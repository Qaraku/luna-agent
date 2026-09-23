package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

// writeRun persists one complete turn, which is what the agent does: the user
// message, one tool call, the assistant message, then the run line.
func writeRun(t *testing.T, s *Store, id, runID, question, answer string, at time.Time) {
	t.Helper()
	if err := s.AppendMessage(id, MessageRecord{RunID: runID, Role: RoleUser, Text: question, At: at}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := s.AppendToolCall(id, ToolCallRecord{RunID: runID, Name: "luna_text_transform", Arguments: `{"text":"hi"}`, Result: "HI", At: at.Add(time.Second)}); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	if err := s.AppendMessage(id, MessageRecord{RunID: runID, Role: RoleAssistant, Text: answer, At: at.Add(2 * time.Second)}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if err := s.AppendRun(id, RunRecord{RunID: runID, StartedAt: at, EndedAt: at.Add(2 * time.Second), Status: StatusOK}); err != nil {
		t.Fatalf("append run: %v", err)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	s := open(t)
	created := time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)
	id, err := s.Create("first question")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := ValidateID(id); err != nil {
		t.Fatalf("created id %q is not valid: %v", id, err)
	}
	writeRun(t, s, id, "run-1", "first question", "first answer", created)

	session, err := s.Read(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if session.ID != id || session.Title != "first question" {
		t.Fatalf("session header: %+v", session)
	}
	if session.CreatedAt.IsZero() {
		t.Fatalf("session has no created_at: %+v", session)
	}
	if session.Truncated {
		t.Fatal("a fully written session reported a truncated tail")
	}
	if session.RunCount != 1 {
		t.Fatalf("run_count=%d, want 1", session.RunCount)
	}
	if want := created.Add(2 * time.Second); !session.UpdatedAt.Equal(want) {
		t.Fatalf("updated_at=%s, want %s", session.UpdatedAt, want)
	}

	wantTypes := []string{TypeSession, TypeMessage, TypeToolCall, TypeMessage, TypeRun}
	if len(session.Records) != len(wantTypes) {
		t.Fatalf("records=%d, want %d: %+v", len(session.Records), len(wantTypes), session.Records)
	}
	for i, want := range wantTypes {
		if session.Records[i].Type != want {
			t.Fatalf("record %d type=%q, want %q", i, session.Records[i].Type, want)
		}
	}
	toolCall := session.Records[2].ToolCall
	if toolCall == nil || toolCall.Name != "luna_text_transform" || toolCall.Arguments != `{"text":"hi"}` || toolCall.Result != "HI" || toolCall.Error != "" || toolCall.RunID != "run-1" {
		t.Fatalf("tool_call record: %+v", toolCall)
	}
	assistant := session.Records[3].Message
	if assistant == nil || assistant.Role != RoleAssistant || assistant.Text != "first answer" {
		t.Fatalf("assistant record: %+v", assistant)
	}
	run := session.Records[4].Run
	if run == nil || run.Status != StatusOK || run.RunID != "run-1" || !run.StartedAt.Equal(created) || !run.EndedAt.Equal(created.Add(2*time.Second)) {
		t.Fatalf("run record: %+v", run)
	}

	messages, err := s.Messages(id)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != RoleUser || messages[0].Text != "first question" || messages[1].Role != RoleAssistant {
		t.Fatalf("messages: %+v", messages)
	}
}

// The line shape is frozen: each record type carries exactly its documented
// fields, so a later reader can depend on the file rather than on this package.
func TestRecordShapeMatchesTheFrozenFormat(t *testing.T) {
	s := open(t)
	id, err := s.Create("shape check")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	writeRun(t, s, id, "run-1", "shape check", "answer", time.Now())

	data, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines=%d: %q", len(lines), string(data))
	}
	want := [][]string{
		{"created_at", "id", "title", "type"},
		{"at", "role", "run_id", "text", "type"},
		{"arguments", "at", "error", "name", "result", "run_id", "type"},
		{"at", "role", "run_id", "text", "type"},
		{"ended_at", "run_id", "started_at", "status", "type"},
	}
	for i, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("line %d is not JSON: %v", i+1, err)
		}
		got := make([]string, 0, len(fields))
		for key := range fields {
			got = append(got, key)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want[i], ",") {
			t.Fatalf("line %d fields=%v, want %v", i+1, got, want[i])
		}
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("the file does not end with a newline")
	}
}

// Restart: a second Store over the same directory reads the same session, which
// is what "the context survives a process restart" means.
func TestSessionSurvivesStoreReconstruction(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id, err := first.Create("durable question")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	writeRun(t, first, id, "run-1", "durable question", "durable answer", time.Now())

	second, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if second.Dir() != first.Dir() {
		t.Fatalf("dir=%q, want %q", second.Dir(), first.Dir())
	}
	session, err := second.Read(id)
	if err != nil {
		t.Fatalf("read after restart: %v", err)
	}
	if session.Title != "durable question" || session.RunCount != 1 || len(session.Records) != 5 {
		t.Fatalf("session after restart: %+v", session)
	}
	messages, err := second.Messages(id)
	if err != nil {
		t.Fatalf("messages after restart: %v", err)
	}
	if len(messages) != 2 || messages[1].Text != "durable answer" {
		t.Fatalf("messages after restart: %+v", messages)
	}
	// The reconstructed store is usable, not just readable.
	if err := second.AppendRun(id, RunRecord{RunID: "run-2", StartedAt: time.Now(), EndedAt: time.Now(), Status: StatusError}); err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	session, err = second.Read(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if session.RunCount != 2 {
		t.Fatalf("run_count=%d, want 2", session.RunCount)
	}
}

func TestUnknownSessionIsAnError(t *testing.T) {
	s := open(t)
	missing := "0123456789abcdef01234567"

	if _, err := s.Read(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read error=%v, want ErrNotFound", err)
	}
	if _, err := s.Messages(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("messages error=%v, want ErrNotFound", err)
	}
	exists, err := s.Exists(missing)
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v, want false and no error", exists, err)
	}
	if err := s.AppendMessage(missing, MessageRecord{Role: RoleUser, Text: "hi"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("append error=%v, want ErrNotFound", err)
	}
	// An append must not create the file it cannot find.
	if _, err := os.Stat(s.path(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an append created a file for an unknown session: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list=%+v, want empty", list)
	}
}

func TestInvalidIDIsRejected(t *testing.T) {
	s := open(t)
	invalid := []string{
		"", "short", "UPPERCASE12345678", "with-dash-12345678", "with.dot.12345678",
		"../../etc/passwd", "0123456789abcdef01234567/", "0123456789abcdef01234567.jsonl",
		strings.Repeat("a", idMaxLen+1), "0123 56789abcdef0",
	}
	for _, id := range invalid {
		if err := ValidateID(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("ValidateID(%q)=%v, want ErrInvalidID", id, err)
		}
		if _, err := s.Read(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Read(%q)=%v, want ErrInvalidID", id, err)
		}
		if _, err := s.Exists(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Exists(%q)=%v, want ErrInvalidID", id, err)
		}
		if err := s.AppendToolCall(id, ToolCallRecord{}); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("AppendToolCall(%q)=%v, want ErrInvalidID", id, err)
		}
	}
	// Nothing an invalid id names may appear in the directory.
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid ids created files: %v", entries)
	}
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") was accepted")
	}
}

// A crash mid-write leaves an unterminated fragment. The complete records
// before it must still read, and the fragment must not be reported as a record.
func TestTruncatedFinalLineKeepsEveryCompleteRecord(t *testing.T) {
	s := open(t)
	id, err := s.Create("crash")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	at := time.Now()
	writeRun(t, s, id, "run-1", "crash", "answer", at)
	before, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	torn := append([]byte{}, before...)
	torn = append(torn, []byte(`{"type":"message","run_id":"run-02","role":"user","text":"tor`)...)
	if err := os.WriteFile(s.path(id), torn, filePerm); err != nil {
		t.Fatalf("write torn file: %v", err)
	}

	session, err := s.Read(id)
	if err != nil {
		t.Fatalf("read session with a torn tail: %v", err)
	}
	if !session.Truncated {
		t.Fatal("a torn tail was not reported")
	}
	if len(session.Records) != 5 || session.RunCount != 1 {
		t.Fatalf("complete records were lost: %+v", session)
	}
	if session.Title != "crash" {
		t.Fatalf("session header lost: %+v", session)
	}
	messages, err := s.Messages(id)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages=%+v, want the two complete ones", messages)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list with a torn tail: %v", err)
	}
	if len(list) != 1 || list[0].RunCount != 1 {
		t.Fatalf("list=%+v", list)
	}
	if len(list[0].ID) == 0 {
		t.Fatalf("list=%+v", list)
	}

	// The writer terminates every record with a newline, so a complete-looking
	// last line without one is still treated as a torn write.
	complete := string(before) + `{"type":"message","run_id":"run-02","role":"user","text":"no newline","at":"` + time.Now().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(s.path(id), []byte(complete), filePerm); err != nil {
		t.Fatalf("write file: %v", err)
	}
	session, err = s.Read(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !session.Truncated || len(session.Records) != 5 {
		t.Fatalf("an unterminated final line must be dropped: truncated=%v records=%d", session.Truncated, len(session.Records))
	}
}

// Appending after a torn write must not merge a new record into the fragment,
// and must not rewrite any complete record.
func TestAppendRepairsATornTail(t *testing.T) {
	s := open(t)
	id, err := s.Create("recover")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	at := time.Now()
	writeRun(t, s, id, "run-1", "recover", "answer", at)
	before, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	torn := append(append([]byte{}, before...), []byte(`{"type":"mess`)...)
	if err := os.WriteFile(s.path(id), torn, filePerm); err != nil {
		t.Fatalf("write torn file: %v", err)
	}

	if err := s.AppendMessage(id, MessageRecord{RunID: "run-2", Role: RoleUser, Text: "after the crash", At: at.Add(time.Minute)}); err != nil {
		t.Fatalf("append after a torn tail: %v", err)
	}
	session, err := s.Read(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if session.Truncated {
		t.Fatal("the torn fragment survived a later append")
	}
	if len(session.Records) != 6 {
		t.Fatalf("records=%d, want 6", len(session.Records))
	}
	last := session.Records[5].Message
	if last == nil || last.Text != "after the crash" {
		t.Fatalf("last record=%+v", session.Records[5])
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.HasPrefix(string(data), string(before)) {
		t.Fatal("an append rewrote a complete record")
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d is not a record: %v", i+1, err)
		}
	}
}

func TestListIsNewestFirstWithRunCounts(t *testing.T) {
	s := open(t)
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := s.Create(fmt.Sprintf("session %d", i))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, id)
		at := base.Add(time.Duration(i) * time.Hour)
		writeRun(t, s, id, fmt.Sprintf("run-%d", i), fmt.Sprintf("session %d", i), "answer", at)
	}
	// The oldest session gets the newest activity, so ordering must follow
	// updated_at rather than creation order.
	if err := s.AppendRun(ids[0], RunRecord{RunID: "run-late", StartedAt: base.Add(24 * time.Hour), EndedAt: base.Add(24 * time.Hour), Status: StatusCancelled}); err != nil {
		t.Fatalf("append run: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list=%+v", list)
	}
	wantOrder := []string{ids[0], ids[2], ids[1]}
	for i, want := range wantOrder {
		if list[i].ID != want {
			t.Fatalf("list[%d]=%s, want %s (list=%+v)", i, list[i].ID, want, list)
		}
	}
	if list[0].RunCount != 2 || list[1].RunCount != 1 || list[2].RunCount != 1 {
		t.Fatalf("run counts=%+v", list)
	}
	if list[0].Title != "session 0" {
		t.Fatalf("title=%q", list[0].Title)
	}
	if want := base.Add(24 * time.Hour); !list[0].UpdatedAt.Equal(want) {
		t.Fatalf("updated_at=%s, want %s", list[0].UpdatedAt, want)
	}
	if len(list[0].Title) == 0 || list[0].UpdatedAt.IsZero() {
		t.Fatalf("summary=%+v", list[0])
	}
}

// Corruption in the middle of a file is never silently skipped: only the
// unterminated tail has a crash model behind it.
func TestInteriorCorruptionIsLoud(t *testing.T) {
	s := open(t)
	id, err := s.Create("corrupt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AppendMessage(id, MessageRecord{RunID: "run-1", Role: RoleUser, Text: "kept", At: time.Now()}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.AppendMessage(id, MessageRecord{RunID: "run-1", Role: RoleAssistant, Text: "tail", At: time.Now()}); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	corrupted := strings.Join([]string{lines[0], `{"type":"message","run_id":`, lines[2]}, "\n") + "\n"
	if err := os.WriteFile(s.path(id), []byte(corrupted), filePerm); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := s.Read(id); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("read error=%v, want ErrCorrupt", err)
	}
	if _, err := s.List(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("list error=%v, want ErrCorrupt", err)
	}

	// An unknown record type is corruption too, not a record to skip.
	unknown := lines[0] + "\n" + `{"type":"profile","id":"x"}` + "\n"
	if err := os.WriteFile(s.path(id), []byte(unknown), filePerm); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := s.Read(id); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("read error=%v, want ErrCorrupt for an unknown type", err)
	}
}

// A session file whose first line was lost to a torn write still has an
// authoritative identity: the file name.
func TestSessionFileWithoutItsSessionLineIsStillReadable(t *testing.T) {
	s := open(t)
	id, err := s.Create("headerless")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AppendMessage(id, MessageRecord{RunID: "run-1", Role: RoleUser, Text: "kept", At: time.Now()}); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	tail := strings.Join([]string{lines[1]}, "\n") + "\n"
	torn := tail + `{"type":"ses`
	if err := os.WriteFile(s.path(id), []byte(torn), filePerm); err != nil {
		t.Fatalf("write file: %v", err)
	}
	session, err := s.Read(id)
	if err != nil {
		t.Fatalf("read a headerless session: %v", err)
	}
	if session.ID != id || session.CreatedAt.IsZero() || !session.Truncated {
		t.Fatalf("session=%+v", session)
	}
	if len(session.Records) != 1 || session.Records[0].Message == nil {
		t.Fatalf("records=%+v", session.Records)
	}
}

// Concurrent runs append to one store. Every line must survive intact.
func TestConcurrentAppendsAreSerialized(t *testing.T) {
	s := open(t)
	id, err := s.Create("concurrent")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const writers, perWriter = 8, 25
	at := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				record := MessageRecord{RunID: fmt.Sprintf("run-%d-%d", w, i), Role: RoleUser, Text: strings.Repeat("x", 200), At: at}
				if err := s.AppendMessage(id, record); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}
	session, err := s.Read(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if session.Truncated {
		t.Fatal("a concurrent append produced a torn tail")
	}
	if len(session.Records) != writers*perWriter+1 {
		t.Fatalf("records=%d, want %d", len(session.Records), writers*perWriter+1)
	}
	seen := map[string]bool{}
	for _, record := range session.Records[1:] {
		seen[record.Message.RunID] = true
	}
	if len(seen) != writers*perWriter {
		t.Fatalf("distinct run ids=%d, want %d", len(seen), writers*perWriter)
	}
}

func TestSessionIDsAreUnguessableRandomStrings(t *testing.T) {
	s := open(t)
	other, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 128; i++ {
		source := s
		if i%2 == 1 {
			source = other
		}
		id, err := source.Create("id check")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := ValidateID(id); err != nil {
			t.Fatalf("id %q: %v", id, err)
		}
		if len(id) != 2*idBytes {
			t.Fatalf("id %q has length %d, want %d", id, len(id), 2*idBytes)
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("id %q is not random hex: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("id %q was handed out twice", id)
		}
		seen[id] = true
	}
}

func TestCreatedFilesArePrivateAndTitled(t *testing.T) {
	s := open(t)
	id, err := s.Create("  line one\nline two\t" + strings.Repeat("z", 200))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(s.path(id))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != filePerm {
		t.Fatalf("file permission=%o, want %o", perm, filePerm)
	}
	session, err := s.Read(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.ContainsAny(session.Title, "\n\t") {
		t.Fatalf("title kept whitespace: %q", session.Title)
	}
	if runes := []rune(session.Title); len(runes) > TitleLimit {
		t.Fatalf("title has %d runes, want at most %d: %q", len(runes), TitleLimit, session.Title)
	}
	if !strings.HasPrefix(session.Title, "line one line two") || !strings.HasSuffix(session.Title, "…") {
		t.Fatalf("title=%q", session.Title)
	}
	if got := TitleText("short  title\n"); got != "short title" {
		t.Fatalf("TitleText=%q", got)
	}
}

// A foreign file in the session directory is ignored; a session file is not.
func TestListIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id, err := s.Create("real")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for name, body := range map[string]string{
		"README.md":        "# notes\n",
		"UPPER12345.jsonl": "{}",
		"1.jsonl":          "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), filePerm); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list=%+v, want only %s", list, id)
	}
}
