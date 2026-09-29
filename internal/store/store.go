// Package store keeps Luna sessions on disk as append-only JSONL files: one
// file per session, one JSON record per line, under a configurable directory.
//
// The record shape is frozen by the S2a spec (`session` / `message` /
// `tool_call` / `run`), because a later reader — the browser replay UI — depends
// on it.
//
// Three properties carry the design:
//
//   - append only. Every line is one Write call terminated by a newline, so a
//     crash can lose the unterminated fragment at the tail of the file and
//     nothing else. No record is ever rewritten; there is no database and no
//     migration.
//   - one writer. Every write takes the store mutex, so concurrent runs cannot
//     interleave lines inside one session.
//   - tolerant reads. An unterminated trailing fragment is dropped (and
//     reported as Session.Truncated) instead of failing the whole session,
//     while a malformed record anywhere else is a loud error, never a silent
//     gap.
//
// A directory belongs to one store: run a single process per session
// directory.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record types. The first four are frozen by the S2a spec; TypeConfig was added
// later, which the frozen format allows: a reader ignores a field it does not
// know, and a new type is a new line, not a changed one.
const (
	TypeSession  = "session"
	TypeMessage  = "message"
	TypeToolCall = "tool_call"
	TypeRun      = "run"
	TypeConfig   = "config"
)

// Run statuses, frozen by the S2a spec.
const (
	StatusOK          = "ok"
	StatusError       = "error"
	StatusCancelled   = "cancelled"
	StatusInterrupted = "interrupted"
)

// Message roles, frozen by the S2a spec.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// FileSuffix is the on-disk name suffix of a session file: <id>.jsonl.
const FileSuffix = ".jsonl"

const (
	// idBytes is the entropy behind a session id. Twelve random bytes make an
	// id a guessable-target only by brute force; ids are never counters and
	// never timestamps.
	idBytes = 12
	// idMinLen and idMaxLen bound what the store will turn into a file name.
	// The charset below is the second half of the same guard: no separator, no
	// dot and no uppercase can reach the filesystem.
	idMinLen = 8
	idMaxLen = 64
	// TitleLimit is the rune cap of a stored session title.
	TitleLimit = 80

	dirPerm  = 0o700
	filePerm = 0o600

	repairChunk = 8192
)

var (
	// ErrNotFound reports a session id with no file behind it.
	ErrNotFound = errors.New("unknown session")
	// ErrInvalidID reports an id that is not a well-formed session id.
	ErrInvalidID = errors.New("invalid session id")
	// ErrCorrupt reports a session file whose records cannot be trusted.
	ErrCorrupt = errors.New("corrupt session file")
)

// SessionRecord is the first line of a session file.
type SessionRecord struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Title     string    `json:"title"`
	// AutoTitle 仅为新建空会话启用，读取时取第一条用户消息，不改写 header。
	AutoTitle bool `json:"auto_title,omitempty"`
}

// MessageRecord is one persisted conversation message.
type MessageRecord struct {
	Type  string    `json:"type"`
	RunID string    `json:"run_id"`
	Role  string    `json:"role"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
}

// ToolCallRecord is one completed tool call. Arguments is the raw JSON text the
// model produced. Plugin identity (generation, version, process id) is
// deliberately absent: the frozen record has no field for it, so identity
// cannot travel from the store back into a model context.
type ToolCallRecord struct {
	Type      string    `json:"type"`
	RunID     string    `json:"run_id"`
	Name      string    `json:"name"`
	Arguments string    `json:"arguments"`
	Result    string    `json:"result"`
	Error     string    `json:"error"`
	At        time.Time `json:"at"`
}

// RunRecord is written once per run, after the run ends, so a crash mid-run
// leaves the run unrecorded rather than half-recorded.
type RunRecord struct {
	Type      string    `json:"type"`
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Status    string    `json:"status"`
	Usage     *Usage    `json:"usage,omitempty"`
}

// ConfigRecord is one statement of which model the session's next run should
// use. Model names an entry of the configured model list; it is a name, not an
// endpoint, so the record cannot carry a credential or a target of its own.
//
// Workspace optionally names the workspace the session works in, by id. It is a
// second field of the same record rather than a second kind of record, because
// it answers the same question the model does — how this session's next run
// should be set up — and the file already has a place for "the newest statement
// wins". A writer that changes one field carries the other one over: a record
// that named only a workspace would otherwise read as "and no model", which is
// how a model choice gets silently erased.
//
// The record is appended, never edited: the newest statement is the one in
// force, and what was chosen before stays in the file as history.
type ConfigRecord struct {
	Type  string `json:"type"`
	Model string `json:"model"`
	// Workspace is the id of the workspace the session works in, or empty when
	// the session is not bound to one. It is an id and not a name, so renaming
	// a workspace does not detach every session that points at it.
	Workspace string `json:"workspace,omitempty"`
	// nil 为继承；指向空字符串为明确不发送，不能与 none 档位混同。
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	// ExecutionMode 仅保存偏好，不能作为执行授权凭证。
	ExecutionMode string            `json:"execution_mode,omitempty"`
	Permissions   *PermissionRecord `json:"permissions,omitempty"`
	At            time.Time         `json:"at"`
}

// Record is one decoded line. Exactly one of the five pointers is set, matching
// Type.
type Record struct {
	Type     string
	Session  *SessionRecord
	Message  *MessageRecord
	ToolCall *ToolCallRecord
	Run      *RunRecord
	Config   *ConfigRecord
}

// MarshalJSON writes a record back in the frozen line shape, including its type
// field, so a replay response carries exactly what is on disk.
func (r Record) MarshalJSON() ([]byte, error) {
	switch {
	case r.Session != nil:
		return json.Marshal(r.Session)
	case r.Message != nil:
		return json.Marshal(r.Message)
	case r.ToolCall != nil:
		return json.Marshal(r.ToolCall)
	case r.Run != nil:
		return json.Marshal(r.Run)
	case r.Config != nil:
		return json.Marshal(r.Config)
	}
	return nil, fmt.Errorf("store: record of type %q carries no payload", r.Type)
}

// Time is the timestamp this record contributes to a session's updated_at.
func (r Record) Time() time.Time {
	switch {
	case r.Session != nil:
		return r.Session.CreatedAt
	case r.Message != nil:
		return r.Message.At
	case r.ToolCall != nil:
		return r.ToolCall.At
	case r.Run != nil:
		return r.Run.EndedAt
	case r.Config != nil:
		return r.Config.At
	}
	return time.Time{}
}

// Session is one session read back from disk.
type Session struct {
	ID        string
	CreatedAt time.Time
	Title     string
	UpdatedAt time.Time
	RunCount  int
	Records   []Record
	// Config is the session's last config record, or nil when the session has
	// never stated one. The records are append-only, so the newest statement is
	// the one in force; the earlier ones stay in Records in file order.
	Config *ConfigRecord
	// Truncated reports that the file's final line was unterminated, so it was
	// dropped as a torn write and is not part of Records.
	Truncated bool
}

// Summary is the list view of a session, without its records.
type Summary struct {
	ID        string
	Title     string
	UpdatedAt time.Time
	RunCount  int
}

// Store is an append-only JSONL session directory.
type Store struct {
	dir string

	// mu serializes every write. It is the single writer side of the store:
	// concurrent runs append whole lines, never interleaved halves.
	mu sync.Mutex
}

// Open prepares dir as a session directory, creating it when missing.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("store: session directory is required")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("store: create session directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("store: stat session directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("store: %s is not a directory", dir)
	}
	return &Store{dir: dir}, nil
}

// Dir is the directory this store writes into.
func (s *Store) Dir() string { return s.dir }

// ValidateID reports whether id can name a session file. The charset is the
// guard that keeps a request path from reaching the filesystem as a path: no
// separator, no dot, no empty string.
func ValidateID(id string) error {
	if len(id) < idMinLen || len(id) > idMaxLen {
		return ErrInvalidID
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') {
			continue
		}
		return ErrInvalidID
	}
	return nil
}

// Create starts a new session and writes its session line. The returned id is
// unguessable: it comes from the system random source, not from a counter or a
// clock, and a collision is retried rather than overwritten.
func (s *Store) Create(title string) (string, error) {
	record := SessionRecord{Type: TypeSession, CreatedAt: time.Now(), Title: TitleText(title), AutoTitle: strings.TrimSpace(title) == ""}
	for attempt := 0; attempt < 4; attempt++ {
		id, err := newID()
		if err != nil {
			return "", err
		}
		record.ID = id
		line, err := encodeLine(record)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		path := s.path(id)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
		if err != nil {
			s.mu.Unlock()
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("store: create session file: %w", err)
		}
		_, writeErr := file.Write(line)
		closeErr := file.Close()
		s.mu.Unlock()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if writeErr != nil {
				return "", fmt.Errorf("store: write session line: %w", writeErr)
			}
			return "", fmt.Errorf("store: close session file: %w", closeErr)
		}
		return id, nil
	}
	return "", errors.New("store: could not allocate an unused session id")
}

// Exists reports whether id names a session file. An invalid id is an error
// rather than a false, so a caller cannot mistake malformed input for absence.
func (s *Store) Exists(id string) (bool, error) {
	if err := ValidateID(id); err != nil {
		return false, err
	}
	_, err := os.Stat(s.path(id))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("store: stat session file: %w", err)
}

// AppendMessage appends one message line to an existing session.
func (s *Store) AppendMessage(id string, record MessageRecord) error {
	record.Type = TypeMessage
	return s.append(id, record)
}

// AppendToolCall appends one tool_call line to an existing session.
func (s *Store) AppendToolCall(id string, record ToolCallRecord) error {
	record.Type = TypeToolCall
	return s.append(id, record)
}

// AppendRun appends one run line to an existing session.
func (s *Store) AppendRun(id string, record RunRecord) error {
	record.Type = TypeRun
	return s.append(id, record)
}

// AppendConfig appends one config line to an existing session: which model the
// session's next run should use. It records the statement; resolving the name
// against the configured models, and refusing one that is not there, happens
// where the run is started.
func (s *Store) AppendConfig(id string, record ConfigRecord) error {
	record.Type = TypeConfig
	return s.append(id, record)
}

// append writes one record as one line. The file is opened without O_CREATE, so
// an unknown session is an error instead of a new file; it is opened read-write
// because a torn tail is inspected and dropped before the append.
func (s *Store) append(id string, record any) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	line, err := encodeLine(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.path(id), os.O_RDWR|os.O_APPEND, filePerm)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("store: open session file: %w", err)
	}
	defer file.Close()
	// A crash can leave an unterminated fragment at the tail. Drop it before
	// appending, so a new record is never merged into a torn one and no
	// complete record is ever rewritten.
	if err := dropTornTail(file); err != nil {
		return fmt.Errorf("store: repair session file: %w", err)
	}
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("store: append record: %w", err)
	}
	return nil
}

// Read returns one session with its complete records in file order.
func (s *Store) Read(id string) (Session, error) {
	if err := ValidateID(id); err != nil {
		return Session{}, err
	}
	return s.readFile(s.path(id), id, true, nil)
}

// Messages returns the session's persisted messages in file order. It is the
// read side the model context is assembled from, so the history is always the
// history on disk rather than anything kept in memory.
func (s *Store) Messages(id string) ([]MessageRecord, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	var messages []MessageRecord
	_, err := s.readFile(s.path(id), id, false, func(record Record) {
		if record.Message != nil {
			messages = append(messages, *record.Message)
		}
	})
	if err != nil {
		return nil, err
	}
	return messages, nil
}

// List returns every session summary, newest updated_at first. Files that are
// not session files are ignored; a session file that cannot be read is an error
// rather than a silently missing row.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("store: read session directory: %w", err)
	}
	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileSuffix) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), FileSuffix)
		if ValidateID(id) != nil {
			continue
		}
		session, err := s.readFile(filepath.Join(s.dir, entry.Name()), id, false, nil)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, Summary{ID: session.ID, Title: session.Title, UpdatedAt: session.UpdatedAt, RunCount: session.RunCount})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		}
		return summaries[i].ID < summaries[j].ID
	})
	return summaries, nil
}

// path is the session file for an already validated id.
func (s *Store) path(id string) string { return filepath.Join(s.dir, id+FileSuffix) }

// readFile 始终校验每条完整记录，但只保留调用方需要的内容：Read 保留全部，
// List 只聚合元数据，Messages 通过 visit 收集消息。三条路径共用相同的解码规则。
func (s *Store) readFile(path, id string, keepRecords bool, visit func(Record)) (Session, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Session{}, fmt.Errorf("store: open session file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Session{}, fmt.Errorf("store: stat session file: %w", err)
	}
	// 没有完整 header 的文件仍用文件名和同一描述符的时间作为回退，不能让
	// 一次崩溃隐藏其他会话。保留空文件与只有残行时 Records 的原有 nil/[] 区别。
	session := Session{ID: id, CreatedAt: info.ModTime(), UpdatedAt: info.ModTime()}
	if keepRecords && info.Size() > 0 {
		session.Records = []Record{}
	}
	headerSeen := false
	autoTitle := false
	// 上限取开始读取时的长度，不追逐并发追加；内容与 metadata 来自同一个
	// 已打开文件，不再重新按路径打开。上限内的未结束片段仍按残行处理。
	truncated, err := scanRecords(io.LimitReader(file, info.Size()), path, func(record Record) {
		if record.Session != nil && !headerSeen {
			session.CreatedAt = record.Session.CreatedAt
			session.Title = record.Session.Title
			autoTitle = record.Session.AutoTitle && session.Title == ""
			headerSeen = true
		}
		if autoTitle && record.Message != nil && record.Message.Role == RoleUser {
			session.Title = TitleText(record.Message.Text)
			autoTitle = false
		}
		if at := record.Time(); !at.IsZero() {
			session.UpdatedAt = at
		}
		if record.Run != nil {
			session.RunCount++
		}
		if record.Config != nil {
			latest := *record.Config
			session.Config = &latest
		}
		if keepRecords {
			session.Records = append(session.Records, record)
		}
		if visit != nil {
			visit(record)
		}
	})
	if err != nil {
		return Session{}, err
	}
	session.Truncated = truncated
	return session, nil
}

// line is the superset of the frozen record fields used for decoding.
type line struct {
	Type            string            `json:"type"`
	ID              string            `json:"id"`
	CreatedAt       time.Time         `json:"created_at"`
	Title           string            `json:"title"`
	RunID           string            `json:"run_id"`
	Role            string            `json:"role"`
	Text            string            `json:"text"`
	At              time.Time         `json:"at"`
	Name            string            `json:"name"`
	Arguments       string            `json:"arguments"`
	Result          string            `json:"result"`
	Error           string            `json:"error"`
	StartedAt       time.Time         `json:"started_at"`
	EndedAt         time.Time         `json:"ended_at"`
	Status          string            `json:"status"`
	Model           string            `json:"model"`
	Workspace       string            `json:"workspace"`
	ReasoningEffort *string           `json:"reasoning_effort"`
	AutoTitle       bool              `json:"auto_title"`
	ExecutionMode   string            `json:"execution_mode"`
	Permissions     *PermissionRecord `json:"permissions"`
	Usage           *Usage            `json:"usage"`
}

// decodeLine decodes one record. Unknown fields are ignored rather than
// rejected: the format is frozen for readers, and ignoring an added field is
// what lets a later slice extend the writer without invalidating old files.
func decodeLine(data []byte) (Record, error) {
	var raw line
	if err := json.Unmarshal(data, &raw); err != nil {
		return Record{}, err
	}
	switch raw.Type {
	case TypeSession:
		return Record{Type: TypeSession, Session: &SessionRecord{Type: raw.Type, ID: raw.ID, CreatedAt: raw.CreatedAt, Title: raw.Title, AutoTitle: raw.AutoTitle}}, nil
	case TypeMessage:
		return Record{Type: TypeMessage, Message: &MessageRecord{Type: raw.Type, RunID: raw.RunID, Role: raw.Role, Text: raw.Text, At: raw.At}}, nil
	case TypeToolCall:
		return Record{Type: TypeToolCall, ToolCall: &ToolCallRecord{Type: raw.Type, RunID: raw.RunID, Name: raw.Name, Arguments: raw.Arguments, Result: raw.Result, Error: raw.Error, At: raw.At}}, nil
	case TypeRun:
		return Record{Type: TypeRun, Run: &RunRecord{Type: raw.Type, RunID: raw.RunID, StartedAt: raw.StartedAt, EndedAt: raw.EndedAt, Status: raw.Status, Usage: raw.Usage}}, nil
	case TypeConfig:
		return Record{Type: TypeConfig, Config: &ConfigRecord{Type: raw.Type, Model: raw.Model, Workspace: raw.Workspace, ReasoningEffort: raw.ReasoningEffort, ExecutionMode: raw.ExecutionMode, Permissions: raw.Permissions, At: raw.At}}, nil
	}
	return Record{}, fmt.Errorf("unknown record type %q", raw.Type)
}

// encodeLine renders one record as a newline-terminated line, so a single Write
// is one complete record even if the process dies immediately afterwards.
func encodeLine(record any) ([]byte, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("store: encode record: %w", err)
	}
	return append(data, '\n'), nil
}

// dropTornTail truncates an unterminated trailing fragment, if any. The file
// position is left untouched: the append happens at the new end of file.
func dropTornTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, size-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	for end := size; end > 0; {
		start := end - repairChunk
		if start < 0 {
			start = 0
		}
		window := make([]byte, end-start)
		if _, err := file.ReadAt(window, start); err != nil {
			return err
		}
		if index := bytes.LastIndexByte(window, '\n'); index >= 0 {
			return file.Truncate(start + int64(index) + 1)
		}
		end = start
	}
	return file.Truncate(0)
}

// newID returns an unguessable session id from the system random source. A
// failure is an error: the spec forbids substituting a clock or a counter.
func newID() (string, error) {
	var buf [idBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("store: read randomness for session id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// TitleText binds a caller-supplied title: whitespace runs (including newlines)
// collapse to single spaces and the result is cut to TitleLimit runes, with the
// cut marked by a trailing ellipsis.
func TitleText(text string) string {
	joined := strings.Join(strings.Fields(text), " ")
	runes := []rune(joined)
	if len(runes) <= TitleLimit {
		return joined
	}
	return string(runes[:TitleLimit-1]) + "…"
}
