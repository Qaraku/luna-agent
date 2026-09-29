package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxSearchDirectoryEntries = 512
const MaxSearchSessions = 64
const MaxSearchFileBytes = 1024 * 1024
const MaxSearchTotalBytes = 8 * 1024 * 1024
const MaxSearchResultBytes = 16 * 1024
const searchHeaderBytes = 16 * 1024
const searchExcerptRunes = 600

type SearchOptions struct {
	Query, SessionID               string
	ExcludeSessionID, ExcludeRunID string
	Workspace                      *string
	Limit                          int
}
type SearchMatch struct {
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title"`
	RunID       string    `json:"run_id,omitempty"`
	Role        string    `json:"role"`
	Text        string    `json:"text"`
	At          time.Time `json:"at"`
	WorkspaceID *string   `json:"workspace_id"`
}
type SearchResult struct {
	Matches      []SearchMatch `json:"matches"`
	ScannedFiles int           `json:"scanned_files"`
	ScannedBytes int64         `json:"scanned_bytes"`
	UnknownScope int           `json:"unknown_scope_skipped"`
	Truncated    bool          `json:"truncated"`
	Limits       []string      `json:"limits"`
}

func (r *SearchResult) limited(name string) {
	r.Truncated = true
	for _, old := range r.Limits {
		if old == name {
			return
		}
	}
	r.Limits = append(r.Limits, name)
}

type searchFile struct {
	id string
	at time.Time
}

// Search 只查询受预算约束的消息窗口，不遍历工具输出，也不把完整日志送回模型。
func (s *Store) Search(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	result := SearchResult{Matches: []SearchMatch{}, Limits: []string{}}
	if err := opts.Validate(); err != nil {
		return result, err
	}
	opts.Query = strings.TrimSpace(opts.Query)
	if opts.Limit == 0 {
		opts.Limit = 10
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	files := []searchFile{}
	if opts.SessionID != "" {
		files = append(files, searchFile{id: opts.SessionID})
	} else {
		dir, err := root.Open(".")
		if err != nil {
			return result, err
		}
		entries, err := dir.ReadDir(MaxSearchDirectoryEntries + 1)
		dir.Close()
		if err != nil && err != io.EOF {
			return result, err
		}
		if len(entries) > MaxSearchDirectoryEntries {
			entries = entries[:MaxSearchDirectoryEntries]
			result.limited("directory_entries")
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if !strings.HasSuffix(entry.Name(), FileSuffix) || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), FileSuffix)
			if ValidateID(id) != nil {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return result, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			files = append(files, searchFile{id: id, at: info.ModTime()})
		}
		sort.Slice(files, func(i, j int) bool {
			if files[i].at.Equal(files[j].at) {
				return files[i].id < files[j].id
			}
			return files[i].at.After(files[j].at)
		})
	}
	if len(files) > MaxSearchSessions {
		files = files[:MaxSearchSessions]
		result.limited("sessions")
	}
	usedResult := 0
	for _, candidate := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		remaining := int64(MaxSearchTotalBytes) - result.ScannedBytes
		if remaining <= 0 {
			result.limited("total_bytes")
			break
		}
		info, err := root.Lstat(candidate.id + FileSuffix)
		if errors.Is(err, os.ErrNotExist) {
			if opts.SessionID != "" {
				return result, ErrNotFound
			}
			continue
		}
		if err != nil {
			return result, err
		}
		if !info.Mode().IsRegular() {
			return result, fmt.Errorf("session history must be a regular file")
		}
		file, err := root.Open(candidate.id + FileSuffix)
		if err != nil {
			return result, err
		}
		info, err = file.Stat()
		if err != nil {
			file.Close()
			return result, err
		}
		allowance := min(info.Size(), int64(MaxSearchFileBytes), remaining)
		partial := info.Size() > allowance
		if partial {
			if allowance == remaining {
				result.limited("total_bytes")
			}
			if info.Size() > MaxSearchFileBytes {
				result.limited("file_bytes")
			}
		}
		data, title, read, err := readSearchWindow(file, info.Size(), allowance, partial)
		file.Close()
		result.ScannedFiles++
		result.ScannedBytes += read
		if err != nil {
			return result, err
		}
		if title == "" {
			title = candidate.id
		}
		if len(data) > 0 && !bytes.HasSuffix(data, []byte{'\n'}) {
			result.limited("incomplete_record")
		}
		hits, unknown, more, err := scanSearchWindow(ctx, data, candidate.id, title, !partial, opts)
		if err != nil {
			return result, err
		}
		result.UnknownScope += unknown
		if unknown > 0 {
			result.limited("unknown_legacy_scope")
		}
		if more {
			result.limited("matches")
		}
		for i := len(hits) - 1; i >= 0; i-- {
			encoded, _ := json.Marshal(hits[i])
			if usedResult+len(encoded) > MaxSearchResultBytes-2048 {
				result.limited("result_bytes")
				return result, nil
			}
			usedResult += len(encoded)
			result.Matches = append(result.Matches, hits[i])
			if len(result.Matches) >= opts.Limit {
				result.limited("matches")
				return result, nil
			}
		}
	}
	return result, nil
}
func readSearchWindow(file *os.File, size, allowance int64, partial bool) (data []byte, title string, read int64, err error) {
	if allowance <= 0 {
		return nil, "", 0, nil
	}
	if !partial {
		data = make([]byte, int(allowance))
		n, e := file.ReadAt(data, 0)
		read = int64(n)
		data = data[:n]
		if e != nil && e != io.EOF {
			err = e
		}
		return
	}
	headerSize := min(int64(searchHeaderBytes), allowance/4)
	head := make([]byte, int(headerSize))
	n, e := file.ReadAt(head, 0)
	read += int64(n)
	if e != nil && e != io.EOF {
		return nil, "", read, e
	}
	if end := bytes.IndexByte(head[:n], '\n'); end >= 0 {
		record, e := decodeLine(head[:end])
		if e != nil {
			return nil, "", read, fmt.Errorf("%w: session header", ErrCorrupt)
		}
		if record.Session != nil {
			title = TitleText(record.Session.Title)
		}
	}
	tailSize := allowance - headerSize
	if tailSize <= 1 {
		return nil, title, read, nil
	}
	tail := make([]byte, int(tailSize))
	n, e = file.ReadAt(tail, size-tailSize)
	read += int64(n)
	if e != nil && e != io.EOF {
		return nil, "", read, e
	}
	if n == 0 {
		return nil, title, read, nil
	}
	// 第一个字节仅用于确认边界；从半行开始时丢掉整段，不把截断 JSON 当作损坏行。
	if tail[0] == '\n' {
		return tail[1:n], title, read, nil
	}
	end := bytes.IndexByte(tail[:n], '\n')
	if end < 0 {
		return nil, title, read, nil
	}
	return tail[end+1 : n], title, read, nil
}
func scanSearchWindow(ctx context.Context, data []byte, id, title string, scopeKnown bool, opts SearchOptions) ([]SearchMatch, int, bool, error) {
	hits := []SearchMatch{}
	unknown := 0
	more := false
	scope := ""
	completeWindow := scopeKnown
	scopeKnown = false
	autoTitle := false
	headerSeen := false
	query := strings.ToLower(opts.Query)
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, unknown, more, err
		}
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		line := data[:end]
		data = data[end+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, err := decodeLine(line)
		if err != nil {
			return nil, unknown, more, fmt.Errorf("%w: %s", ErrCorrupt, id)
		}
		if record.Session != nil && !headerSeen {
			headerSeen = true
			if completeWindow && !scopeKnown {
				scopeKnown = true
				scope = ""
			}
			title = TitleText(record.Session.Title)
			autoTitle = record.Session.AutoTitle && title == ""
		}
		if record.Config != nil {
			scope = record.Config.Workspace
			scopeKnown = true
			if record.Config.TitleOverride != nil {
				title = TitleText(*record.Config.TitleOverride)
			}
		}
		message := record.Message
		if message != nil && opts.ExcludeRunID != "" && id == opts.ExcludeSessionID && message.RunID == opts.ExcludeRunID {
			continue
		}
		if autoTitle && message != nil && message.Role == RoleUser {
			title = TitleText(message.Text)
			autoTitle = false
		}
		if message == nil || (message.Role != RoleUser && message.Role != RoleAssistant) || !strings.Contains(strings.ToLower(message.Text), query) {
			continue
		}
		workspace := message.WorkspaceID
		if workspace == nil && scopeKnown {
			current := scope
			workspace = &current
		}
		if opts.Workspace != nil {
			if workspace == nil {
				unknown++
				continue
			}
			if *workspace != *opts.Workspace {
				continue
			}
		}
		hit := SearchMatch{SessionID: id, Title: title, RunID: message.RunID, Role: message.Role, Text: searchExcerpt(message.Text, query), At: message.At, WorkspaceID: workspace}
		if len(hits) >= opts.Limit {
			copy(hits, hits[1:])
			hits = hits[:len(hits)-1]
			more = true
		}
		hits = append(hits, hit)
	}
	if title == "" {
		title = id
	}
	for i := range hits {
		hits[i].Title = title
	}
	return hits, unknown, more, nil
}
func searchExcerpt(text, query string) string {
	lower := strings.ToLower(text)
	at := strings.Index(lower, query)
	if at < 0 {
		return ""
	}
	offset := utf8.RuneCountInString(lower[:at])
	runes := []rune(text)
	start := max(0, offset-80)
	end := min(len(runes), start+searchExcerptRunes)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func (opts SearchOptions) Validate() error {
	query := strings.TrimSpace(opts.Query)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 256 {
		return fmt.Errorf("query must contain 1..256 characters")
	}
	if opts.Limit < 0 || opts.Limit > 50 {
		return fmt.Errorf("limit must be 1..50")
	}
	if opts.SessionID != "" {
		return ValidateID(opts.SessionID)
	}
	return nil
}
