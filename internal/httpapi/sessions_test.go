package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/store"
)

// recordingRunner captures the run request the handler admitted, so a test can
// prove which session a run belongs to. It mirrors the real runner's contract:
// the run id and the session id travel on the existing run.started event.
type recordingRunner struct {
	mu       sync.Mutex
	requests []agent.RunRequest
}

func (r *recordingRunner) Run(_ context.Context, req agent.RunRequest) (string, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	req.Sink.Emit(agent.Event{Type: "run.started", Data: agent.RunStarted{RunID: req.RunID, SessionID: req.SessionID}})
	req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "hello"}})
	req.Sink.Emit(agent.Event{Type: "run.finished", Data: agent.RunFinished{RunID: req.RunID, Answer: "hello"}})
	return "hello", nil
}

func (r *recordingRunner) last() (agent.RunRequest, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		return agent.RunRequest{}, false
	}
	return r.requests[len(r.requests)-1], true
}

func seedSession(t *testing.T, s *store.Store, title string) string {
	t.Helper()
	id, err := s.Create(title)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id
}

// seedTurn writes one complete turn into a session, the way the agent does.
func seedTurn(t *testing.T, s *store.Store, id, runID, question, answer string, at time.Time) {
	t.Helper()
	if err := s.AppendMessage(id, store.MessageRecord{RunID: runID, Role: store.RoleUser, Text: question, At: at}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := s.AppendToolCall(id, store.ToolCallRecord{RunID: runID, Name: "luna_text_transform", Arguments: `{"text":"hi"}`, Result: "HI", At: at.Add(time.Second)}); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	if err := s.AppendMessage(id, store.MessageRecord{RunID: runID, Role: store.RoleAssistant, Text: answer, At: at.Add(2 * time.Second)}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if err := s.AppendRun(id, store.RunRecord{RunID: runID, StartedAt: at, EndedAt: at.Add(2 * time.Second), Status: store.StatusOK}); err != nil {
		t.Fatalf("append run: %v", err)
	}
}

func TestSessionsListIsNewestFirstWithTheFrozenFields(t *testing.T) {
	sessions := newTestStore(t)
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	older := seedSession(t, sessions, "older question")
	newer := seedSession(t, sessions, "newer question")
	seedTurn(t, sessions, newer, "run-new", "newer question", "answer", base.Add(4*time.Hour))
	seedTurn(t, sessions, older, "run-old", "older question", "answer", base)

	h := handlerWithStore(t, fakeRunner{}, sessions)
	w := request(t, h, http.MethodGet, "/api/sessions", "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Sessions []struct {
			ID        string    `json:"id"`
			Title     string    `json:"title"`
			UpdatedAt time.Time `json:"updated_at"`
			RunCount  int       `json:"run_count"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("sessions=%+v", body.Sessions)
	}
	if body.Sessions[0].ID != newer || body.Sessions[1].ID != older {
		t.Fatalf("order=%+v, want newest first", body.Sessions)
	}
	if body.Sessions[0].Title != "newer question" || body.Sessions[0].RunCount != 1 {
		t.Fatalf("newest summary=%+v", body.Sessions[0])
	}
	if body.Sessions[0].UpdatedAt.IsZero() {
		t.Fatalf("updated_at=%s", body.Sessions[0].UpdatedAt)
	}
	// The frozen wire shape: exactly these four fields per entry.
	var raw struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, entry := range raw.Sessions {
		for _, key := range []string{"id", "title", "updated_at", "run_count"} {
			if _, ok := entry[key]; !ok {
				t.Fatalf("entry %v is missing %q", entry, key)
			}
		}
		if len(entry) != 4 {
			t.Fatalf("entry has extra fields: %v", entry)
		}
	}
}

func TestSessionReadReplaysTheFullRecord(t *testing.T) {
	sessions := newTestStore(t)
	id := seedSession(t, sessions, "replay me")
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	seedTurn(t, sessions, id, "run-1", "replay me", "answer", at)

	h := handlerWithStore(t, fakeRunner{}, sessions)
	w := request(t, h, http.MethodGet, "/api/sessions/"+id, "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		ID        string           `json:"id"`
		Title     string           `json:"title"`
		CreatedAt time.Time        `json:"created_at"`
		UpdatedAt time.Time        `json:"updated_at"`
		RunCount  int              `json:"run_count"`
		Truncated bool             `json:"truncated"`
		Records   []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if body.ID != id || body.Title != "replay me" || body.RunCount != 1 || body.Truncated {
		t.Fatalf("detail=%+v", body)
	}
	wantTypes := []string{"session", "message", "tool_call", "message", "run"}
	if len(body.Records) != len(wantTypes) {
		t.Fatalf("records=%v", body.Records)
	}
	for i, want := range wantTypes {
		if body.Records[i]["type"] != want {
			t.Fatalf("record %d=%v, want type %q", i, body.Records[i], want)
		}
	}
	if body.Records[2]["name"] != "luna_text_transform" || body.Records[2]["arguments"] != `{"text":"hi"}` || body.Records[2]["result"] != "HI" {
		t.Fatalf("tool_call record=%v", body.Records[2])
	}
	if body.Records[4]["status"] != "ok" || body.Records[4]["run_id"] != "run-1" {
		t.Fatalf("run record=%v", body.Records[4])
	}
	// A replay carries no plugin identity: the frozen record has no field for it.
	if strings.Contains(w.Body.String(), "plugin_pid") || strings.Contains(w.Body.String(), "generation") {
		t.Fatalf("session replay leaked plugin identity: %s", w.Body.String())
	}
}

func TestRunWithoutSessionStartsOneAndCarriesItsIdOnRunStarted(t *testing.T) {
	sessions := newTestStore(t)
	runner := &recordingRunner{}
	h := handlerWithStore(t, runner, sessions)

	w := request(t, h, http.MethodPost, "/api/runs", `{"message":"start a session"}`, true)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	req, ok := runner.last()
	if !ok {
		t.Fatal("runner was not called")
	}
	if req.SessionID == "" {
		t.Fatal("the run reached the runner without a session id")
	}
	if req.Message != "start a session" {
		t.Fatalf("message=%q", req.Message)
	}
	if err := store.ValidateID(req.SessionID); err != nil {
		t.Fatalf("session id %q is not valid: %v", req.SessionID, err)
	}
	if _, err := hex.DecodeString(req.SessionID); err != nil {
		t.Fatalf("session id %q is not an unguessable random string: %v", req.SessionID, err)
	}
	if !strings.Contains(body, `"session_id":"`+req.SessionID+`"`) {
		t.Fatalf("run.started does not carry the session id: %s", body)
	}
	if !strings.Contains(body, `event: run.started`) {
		t.Fatalf("body=%s", body)
	}
	// The early event is where the id travels, and the terminal contract is
	// unchanged: exactly one terminal event, of the existing types.
	if strings.Count(body, "event: run.finished") != 1 || strings.Contains(body, "event: run.failed") {
		t.Fatalf("terminal semantics: %s", body)
	}
	list, err := sessions.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != req.SessionID || list[0].Title != "start a session" {
		t.Fatalf("list=%+v, want the session the run created", list)
	}
}

func TestRunUsesASuppliedSession(t *testing.T) {
	sessions := newTestStore(t)
	id := seedSession(t, sessions, "existing session")
	seedTurn(t, sessions, id, "run-1", "existing session", "answer", time.Now())
	runner := &recordingRunner{}
	h := handlerWithStore(t, runner, sessions)

	body := `{"message":"continue","session_id":"` + id + `"}`
	w := request(t, h, http.MethodPost, "/api/runs", body, true)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	req, _ := runner.last()
	if req.SessionID != id {
		t.Fatalf("session id=%q, want %q", req.SessionID, id)
	}
	if !strings.Contains(w.Body.String(), `"session_id":"`+id+`"`) {
		t.Fatalf("run.started does not carry the session id: %s", w.Body.String())
	}
	list, err := sessions.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("a supplied session id created another session: %+v", list)
	}
	// The run's activity moves the session to the top of the list.
	if list[0].ID != id {
		t.Fatalf("list=%+v", list)
	}
}

func TestUnknownSessionIDIsAClearRejection(t *testing.T) {
	sessions := newTestStore(t)
	runner := &recordingRunner{}
	h := handlerWithStore(t, runner, sessions)

	// A well-formed id with no session behind it: 404, refused before admission,
	// and never a silently created session.
	missing := "0123456789abcdef01234567"
	w := request(t, h, http.MethodPost, "/api/runs", `{"message":"hi","session_id":"`+missing+`"}`, true)
	if w.Code != 404 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "event: ") {
		t.Fatalf("an unknown session id started a stream: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown session") {
		t.Fatalf("error does not name the reason: %s", w.Body.String())
	}
	list, err := sessions.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("an unknown session id created a session: %+v", list)
	}
	if _, ok := runner.last(); ok {
		t.Fatal("an unknown session id reached the runner")
	}

	// A malformed id is refused before it can name a file.
	for _, bad := range []string{"../../etc/passwd", "SHORT", strings.Repeat("a", 100), "0123456789abcdef0123456"} {
		body := `{"message":"hi","session_id":"` + bad + `"}`
		w := request(t, h, http.MethodPost, "/api/runs", body, true)
		if w.Code != 400 && w.Code != 404 {
			t.Fatalf("session_id %q: status=%d body=%s", bad, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "event: ") {
			t.Fatalf("session_id %q started a stream: %s", bad, w.Body.String())
		}
	}
	entries, err := os.ReadDir(sessions.Dir())
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejections created files: %v", entries)
	}
}

func TestSessionEndpointsRejectUnknownMethodsAndIDs(t *testing.T) {
	sessions := newTestStore(t)
	id := seedSession(t, sessions, "existing")
	h := handlerWithStore(t, fakeRunner{}, sessions)

	cases := []struct {
		name, method, path string
		want               int
	}{
		{"unknown id", http.MethodGet, "/api/sessions/0123456789abcdef01234567", 404},
		{"malformed id", http.MethodGet, "/api/sessions/UPPER1234567890", 400},
		{"traversal id", http.MethodGet, "/api/sessions/..%2F..%2Fetc%2Fpasswd", 400},
		{"trailing slash", http.MethodGet, "/api/sessions/", 404},
		{"list rejects post", http.MethodPost, "/api/sessions", 405},
		{"read rejects post", http.MethodPost, "/api/sessions/" + id, 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, h, tc.method, tc.path, "", tc.method == http.MethodPost)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	// The existing session still reads.
	w := request(t, h, http.MethodGet, "/api/sessions/"+id, "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// A torn final line must not hide the complete records of a session over HTTP
// either; the response says so instead of pretending the file is whole.
func TestSessionReadSurfacesATornTail(t *testing.T) {
	sessions := newTestStore(t)
	id := seedSession(t, sessions, "torn")
	seedTurn(t, sessions, id, "run-1", "torn", "answer", time.Now())
	path := sessions.Dir() + "/" + id + store.FileSuffix
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if err := os.WriteFile(path, append(data, []byte(`{"type":"message","run`)...), 0o600); err != nil {
		t.Fatalf("write torn file: %v", err)
	}

	h := handlerWithStore(t, fakeRunner{}, sessions)
	w := request(t, h, http.MethodGet, "/api/sessions/"+id, "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Truncated bool             `json:"truncated"`
		RunCount  int              `json:"run_count"`
		Records   []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Truncated {
		t.Fatal("the response did not report the torn tail")
	}
	if len(body.Records) != 5 || body.RunCount != 1 {
		t.Fatalf("complete records were lost: %+v", body)
	}
}

// The session id of the active run is visible in /api/state without changing any
// existing field.
func TestStateReportsTheCurrentSession(t *testing.T) {
	sessions := newTestStore(t)
	h := handlerWithStore(t, fakeRunner{}, sessions)
	w := request(t, h, http.MethodGet, "/api/state", "", false)
	var state State
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.CurrentSessionID != "" {
		t.Fatalf("idle state reports a session: %q", state.CurrentSessionID)
	}
	if state.Busy {
		t.Fatal("idle state reports a busy host")
	}
	if !state.ModelConfigured || state.Model != "fake-model" {
		t.Fatalf("existing state fields changed: %+v", state)
	}
}
