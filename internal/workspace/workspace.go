// Package workspace 是 Workspace 的数据模型：一组当前工作相关的目录。
//
// Workspace 是**一组目录**，可以是一个也可以是几个：它回答的是“这次工作牵涉哪些
// 目录”，用于项目上下文发现、文件工具的默认范围、项目级 instructions/config/skills
// 的查找，以及以后 terminal 的工作上下文。它**不是 Permission**：它不限制模型能读写
// 什么，只陈述这次工作在哪些目录里进行。
//
// 与 `internal/settings` 的分工：settings 记录用户在产品里做出的选择，这里记录用户
// 定义的工作区本身——它是数据（用户丢了会心疼），所以放在 `layout.Paths.Data` 下，
// 而不是配置文件里。
//
// 存储形状是一个 JSON 文件，整文件重写（走 `internal/atomicfile`）：只有一个进程写
// 它，条目少到一次重写没有代价，而只追加的日志式文件会让“改名”“删目录”变成一件需要
// 重放历史的事。未知字段被拒绝，理由与 settings 相同：文件里一个没人认识的键要么是
// 一次不会生效的手改，要么是这个版本不认识的字段，两种都是用户需要听到的事。
package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Qaraku/luna-agent/internal/atomicfile"
)

// FileName is the file Luna reads and writes inside the user's data directory.
// It is a name, not a path: where the directory is belongs to internal/layout.
const FileName = "workspaces.json"

// IDBytes is how much randomness an identity carries: 12 bytes, rendered as 24
// hexadecimal characters. That is the same width session ids use, so one id is
// not mistaken for another kind of id by looking at it.
const IDBytes = 12

// fileMode is the permission of the file Luna writes. A workspace names
// directories on this machine, which is nobody else's business.
const fileMode fs.FileMode = 0o600

// ErrNameTaken reports a workspace whose name another workspace already uses.
// The name is a label the user reads and may change; it is not a key.
var ErrNameTaken = errors.New("a workspace by that name already exists")

// ErrNotFound reports an id no workspace uses.
var ErrNotFound = errors.New("no such workspace")

// Workspace is one set of directories work is happening in.
type Workspace struct {
	// ID identifies the workspace. It is random rather than derived from the
	// name, because the name is the part a user edits: a session that pointed
	// at "luna" by name would follow whatever that name meant on the day it
	// was read. Names may also change and repeat over time; an id never does.
	ID string `json:"id"`
	// Name is the label the user reads. It is unique among the workspaces on
	// this machine, which is what makes it usable in a list, and it defaults
	// to the first directory's own name so a workspace made from one directory
	// needs no naming at all.
	Name string `json:"name"`
	// Dirs are the directories this workspace names, as absolute paths, in the
	// order the user gave them. This is the only place absolute paths are
	// stored; what reaches the model is the directory names (see
	// internal/plugins/workspace).
	Dirs []string `json:"dirs"`
}

// New builds a workspace from a name and a set of directories.
//
// Every directory has to be absolute: a relative path would mean something
// different depending on where the process was started, and a workspace is
// meant to say the same thing every time it is read back. Duplicates are
// dropped rather than refused — the same directory named twice is a slip, not a
// statement — and the order the user gave is kept, because it is the order the
// directories are presented in. At least one directory is required: a workspace
// with none says nothing about where work is happening.
//
// An empty name is filled in from the first directory. A name that is still
// blank after trimming is refused rather than defaulted, because a label the
// user asked to be blank is not the same request as no label at all.
func New(name string, dirs []string) (Workspace, error) {
	cleaned, err := cleanDirs(dirs)
	if err != nil {
		return Workspace{}, err
	}
	label := strings.TrimSpace(name)
	if label == "" {
		label = filepath.Base(cleaned[0])
	}
	if label == "" || label == "." || label == string(filepath.Separator) {
		return Workspace{}, fmt.Errorf("workspace name %q is not a usable label", name)
	}
	id, err := newID()
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{ID: id, Name: label, Dirs: cleaned}, nil
}

// cleanDirs validates and normalizes a set of directories. It states the reason
// for each refusal, because a workspace that cannot be created is something the
// user has to be able to fix from the message alone.
func cleanDirs(dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("a workspace needs at least one directory")
	}
	seen := make(map[string]bool, len(dirs))
	cleaned := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		trimmed := strings.TrimSpace(dir)
		if trimmed == "" {
			return nil, fmt.Errorf("a workspace directory cannot be blank")
		}
		if !filepath.IsAbs(trimmed) {
			return nil, fmt.Errorf("workspace directory %q is not an absolute path", trimmed)
		}
		path := filepath.Clean(trimmed)
		if seen[path] {
			continue
		}
		seen[path] = true
		cleaned = append(cleaned, path)
	}
	return cleaned, nil
}

// newID returns a fresh workspace id. The randomness is the identity: two
// workspaces named the same thing on two machines are not the same workspace,
// and a session points at an id for that reason.
func newID() (string, error) {
	buf := make([]byte, IDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate workspace id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// file is the on-disk shape of the whole file.
type file struct {
	Workspaces []Workspace `json:"workspaces"`
}

// Store is the workspaces as this process sees them: the file's contents held in
// memory, and the file rewritten whole whenever they change.
//
// Only one process runs Luna, so a whole rewrite is simpler than appending and
// cannot leave a half-updated set behind. The mutex is what makes that true
// inside the process: two requests creating a workspace at the same time must
// not each write a file missing the other's entry.
type Store struct {
	path string

	mu    sync.Mutex
	items []Workspace
}

// Open reads the workspaces file at path.
//
// A file that is not there is not an error: a machine where nobody has defined
// a workspace yet is the normal starting state, and refusing to start over it
// would be inventing a problem. A file that is there but cannot be understood
// is an error: treating a broken file as an empty one would silently lose the
// workspaces it names.
func Open(path string) (*Store, error) {
	items, _, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, items: items}, nil
}

// Load reads the workspaces file at path. The second return value reports
// whether a file was found, the same shape internal/settings uses, so a caller
// can say which source it used.
func Load(path string) ([]Workspace, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", filepath.Base(path), reason(err))
	}
	var decoded file
	decoder := json.NewDecoder(bytes.NewReader(data))
	// Unknown fields are refused, not ignored: this file is written by Luna, so
	// a key nobody recognises is either a hand edit that will not take effect
	// or a field this version does not know — both are things to hear about
	// rather than lose silently.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty file is an empty set, not a broken one.
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return decoded.Workspaces, true, nil
}

// List returns the workspaces in the order they were defined. The result is a
// copy: a caller cannot reach into the store's memory by keeping the slice.
func (s *Store) List() []Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Workspace(nil), s.items...)
}

// Get returns the workspace with this id. found is false when no workspace uses
// it, which is an answer, not a failure.
func (s *Store) Get(id string) (Workspace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID == id {
			return item, true
		}
	}
	return Workspace{}, false
}

// Create defines a new workspace and returns it.
//
// The name must not be one another workspace already uses: the name is what the
// user picks from, so two workspaces sharing one would make the label useless.
// The error names the workspace that holds the name, so the user can find the
// one they meant. Nothing is written when validation fails.
func (s *Store) Create(name string, dirs []string) (Workspace, error) {
	created, err := New(name, dirs)
	if err != nil {
		return Workspace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.Name == created.Name {
			return Workspace{}, fmt.Errorf("%w: %q is used by workspace %s", ErrNameTaken, created.Name, item.ID)
		}
	}
	next := append(append([]Workspace(nil), s.items...), created)
	if err := save(s.path, next); err != nil {
		return Workspace{}, err
	}
	s.items = next
	return created, nil
}

// save rewrites the whole file through the same atomic write the user's
// preferences use. The file is small enough that a whole rewrite costs nothing,
// and a reader either sees the previous set or the new one.
func save(path string, items []Workspace) error {
	if items == nil {
		items = []Workspace{}
	}
	data, err := json.MarshalIndent(file{Workspaces: items}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspaces: %w", err)
	}
	return atomicfile.WriteFile(path, append(data, '\n'), fileMode)
}

// reason reduces a path error to its underlying reason, so an error about a
// workspaces file does not echo the host's directory layout.
func reason(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
