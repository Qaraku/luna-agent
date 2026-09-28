package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/workspace"
)

// countingRunner records how many runs reached it, so a test can tell "refused
// before anything started" apart from "answered with an error after starting".
type countingRunner struct{ runs int }

func (r *countingRunner) Run(_ context.Context, req agent.RunRequest) (string, error) {
	r.runs++
	req.Sink.Emit(agent.Event{Type: "run.started", Data: agent.RunStarted{RunID: req.RunID, SessionID: req.SessionID}})
	req.Sink.Emit(agent.Event{Type: "run.finished", Data: agent.RunFinished{RunID: req.RunID, Answer: "hello"}})
	return "hello", nil
}

// serverWithWorkspaces is handlerWithWorkspaces with a runner the test can count
// through, for the cases where "did anything start at all" is the question.
func serverWithWorkspaces(t *testing.T, runs Runner) (http.Handler, *store.Store, *workspace.Store) {
	t.Helper()
	sessions := newTestStore(t)
	items, err := workspace.Open(filepath.Join(t.TempDir(), workspace.FileName))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	p := &fakePlugins{state: everyAllowlistedTool()}
	h := New(p, runs, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithWorkspaces(items))
	return h, sessions, items
}

// handlerWithWorkspaces builds a server over a real workspace store in a
// temporary file, so the wire behaviour is tested against the store the
// composition root uses rather than a stand-in.
func handlerWithWorkspaces(t *testing.T) (http.Handler, *store.Store, *workspace.Store) {
	t.Helper()
	sessions := newTestStore(t)
	items, err := workspace.Open(filepath.Join(t.TempDir(), workspace.FileName))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	p := &fakePlugins{state: everyAllowlistedTool()}
	h := New(p, fakeRunner{}, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithWorkspaces(items))
	return h, sessions, items
}

func decodeWorkspace(t *testing.T, body []byte) workspaceView {
	t.Helper()
	var view workspaceView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode workspace %s: %v", body, err)
	}
	return view
}

func TestListWorkspacesIsEmptyWhenNoStoreIsConfigured(t *testing.T) {
	h := handlerWithStore(t, fakeRunner{}, newTestStore(t))
	w := request(t, h, http.MethodGet, "/api/workspaces", "", false)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got workspacesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if len(got.Workspaces) != 0 {
		t.Fatalf("workspaces = %v, want none", got.Workspaces)
	}
}

func TestDefiningAWorkspaceNeedsAnOrigin(t *testing.T) {
	h, _, _ := handlerWithWorkspaces(t)
	w := request(t, h, http.MethodPost, "/api/workspaces", `{"name":"luna","dirs":["/tmp"]}`, false)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403 without Origin", w.Code)
	}
}

func TestDefineWorkspaceThenListIt(t *testing.T) {
	dir := t.TempDir()
	h, _, _ := handlerWithWorkspaces(t)
	w := request(t, h, http.MethodPost, "/api/workspaces", `{"name":"luna","dirs":["`+dir+`"]}`, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	created := decodeWorkspace(t, w.Body.Bytes())
	if created.ID == "" || created.Name != "luna" || len(created.Dirs) != 1 || created.Dirs[0] != dir {
		t.Fatalf("created = %+v", created)
	}
	listed := request(t, h, http.MethodGet, "/api/workspaces", "", false)
	var got workspacesResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", listed.Body, err)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].ID != created.ID {
		t.Fatalf("listed = %+v, want the created workspace", got.Workspaces)
	}
}

func TestDefineWorkspaceRefusalsNameTheProblem(t *testing.T) {
	dir := t.TempDir()
	h, _, _ := handlerWithWorkspaces(t)
	if w := request(t, h, http.MethodPost, "/api/workspaces", `{"name":"rel","dirs":["relative"]}`, true); w.Code != 400 {
		t.Fatalf("relative directory: status = %d, want 400", w.Code)
	}
	if w := request(t, h, http.MethodPost, "/api/workspaces", `{"name":"","dirs":[]}`, true); w.Code != 400 {
		t.Fatalf("no directories: status = %d, want 400", w.Code)
	}
	if w := request(t, h, http.MethodPost, "/api/workspaces", `{"name":"one","dirs":["`+dir+`"]}`, true); w.Code != 200 {
		t.Fatalf("first definition: status = %d, body %s", w.Code, w.Body)
	}
	w := request(t, h, http.MethodPost, "/api/workspaces", `{"name":"one","dirs":["`+dir+`"]}`, true)
	if w.Code != 409 {
		t.Fatalf("repeated name: status = %d, want 409", w.Code)
	}
}

func TestSessionWorkspaceKeepsTheModelChoice(t *testing.T) {
	dir := t.TempDir()
	h, sessions, items := handlerWithWorkspaces(t)
	id := seedSession(t, sessions, "a session")
	if err := sessions.AppendConfig(id, store.ConfigRecord{Type: store.TypeConfig, Model: "chosen-model", At: time.Now()}); err != nil {
		t.Fatalf("append config: %v", err)
	}
	created, err := items.Create("luna", []string{dir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", `{"workspace":"`+created.ID+`"}`, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	session, err := sessions.Read(id)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if session.Config == nil || session.Config.Model != "chosen-model" || session.Config.Workspace != created.ID {
		t.Fatalf("config = %+v, want model kept and workspace set", session.Config)
	}
}

func TestChoosingAModelKeepsTheWorkspaceBinding(t *testing.T) {
	dir := t.TempDir()
	h, sessions, items := handlerWithWorkspaces(t)
	id := seedSession(t, sessions, "a session")
	created, err := items.Create("luna", []string{dir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", `{"workspace":"`+created.ID+`"}`, true); w.Code != 200 {
		t.Fatalf("bind: status = %d, body %s", w.Code, w.Body)
	}
	if w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/model", `{"model":"fake-model"}`, true); w.Code != 200 {
		t.Fatalf("choose model: status = %d, body %s", w.Code, w.Body)
	}
	session, err := sessions.Read(id)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if session.Config == nil || session.Config.Workspace != created.ID || session.Config.Model != "fake-model" {
		t.Fatalf("config = %+v, want workspace kept and model changed", session.Config)
	}
}

func TestSessionReplayReportsTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	h, sessions, items := handlerWithWorkspaces(t)
	created, err := items.Create("luna", []string{dir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	unbound := seedSession(t, sessions, "nothing")
	w := request(t, h, http.MethodGet, "/api/sessions/"+unbound, "", false)
	var detail struct {
		Workspace *workspaceView `json:"workspace"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if detail.Workspace != nil {
		t.Fatalf("unbound session reported workspace %+v", detail.Workspace)
	}

	bound := seedSession(t, sessions, "something")
	if w := request(t, h, http.MethodPost, "/api/sessions/"+bound+"/workspace", `{"workspace":"`+created.ID+`"}`, true); w.Code != 200 {
		t.Fatalf("bind: status = %d, body %s", w.Code, w.Body)
	}
	w = request(t, h, http.MethodGet, "/api/sessions/"+bound, "", false)
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if detail.Workspace == nil || detail.Workspace.ID != created.ID || detail.Workspace.Name != "luna" {
		t.Fatalf("bound session reported workspace %+v", detail.Workspace)
	}

	// Clearing the binding is a request that names no workspace, and the replay
	// object goes back to null.
	if w := request(t, h, http.MethodPost, "/api/sessions/"+bound+"/workspace", `{"workspace":""}`, true); w.Code != 200 {
		t.Fatalf("clear: status = %d, body %s", w.Code, w.Body)
	}
	w = request(t, h, http.MethodGet, "/api/sessions/"+bound, "", false)
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if detail.Workspace != nil {
		t.Fatalf("cleared session reported workspace %+v", detail.Workspace)
	}
}

func TestSessionWorkspaceRefusesUnknownTargets(t *testing.T) {
	h, sessions, _ := handlerWithWorkspaces(t)
	id := seedSession(t, sessions, "a session")
	if w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", `{"workspace":"0123456789abcdef01234567"}`, true); w.Code != 404 {
		t.Fatalf("unknown workspace: status = %d, want 404", w.Code)
	}
	if w := request(t, h, http.MethodPost, "/api/sessions/0123456789abcdef01234567/workspace", `{"workspace":""}`, true); w.Code != 404 {
		t.Fatalf("unknown session: status = %d, want 404", w.Code)
	}
	if w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", `{"workspace":""}`, false); w.Code != 403 {
		t.Fatalf("no Origin: status = %d, want 403", w.Code)
	}
}

// A binding that no longer names a workspace fails the run instead of falling back,
// and it fails before anything has started. The fallback is the configured root, a
// different and possibly wider place to read, so a session that asked for one
// directory must not quietly get the whole checkout — and because the refusal happens
// at admission, no run, no stream and no session record come out of it either.
func TestARunBoundToAWorkspaceThatIsGoneIsRefusedBeforeItStarts(t *testing.T) {
	runs := &countingRunner{}
	h, sessions, _ := serverWithWorkspaces(t, runs)
	id := seedSession(t, sessions, "bound to something gone")
	const gone = "0123456789abcdef01234567"
	if err := sessions.AppendConfig(id, store.ConfigRecord{Type: store.TypeConfig, Workspace: gone}); err != nil {
		t.Fatalf("append config: %v", err)
	}

	w := request(t, h, http.MethodPost, "/api/runs", `{"message":"do it","session_id":"`+id+`"}`, true)
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), gone) {
		t.Fatalf("body = %s, want it to name the workspace so the user knows what to unbind", w.Body)
	}
	if runs.runs != 0 {
		t.Fatalf("runs = %d, want the refusal to happen before anything started", runs.runs)
	}
}

// A workspace whose directory was deleted is a different thing from a workspace that
// is gone, and the boundary is deliberately here: the binding still names a
// workspace, so the run starts, and the vanished directory surfaces per call — a file
// tool says it cannot find it — instead of turning into a broken binding. Pinned so
// that this stays a decision rather than drifting into either direction.
func TestAWorkspaceDirectoryThatVanishedDoesNotBreakTheBinding(t *testing.T) {
	dir := t.TempDir()
	runs := &countingRunner{}
	h, sessions, items := serverWithWorkspaces(t, runs)
	created, err := items.Create("luna", []string{dir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	id := seedSession(t, sessions, "bound to a directory that goes away")
	if w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", `{"workspace":"`+created.ID+`"}`, true); w.Code != 200 {
		t.Fatalf("bind: status = %d, body %s", w.Code, w.Body)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove the directory: %v", err)
	}

	w := request(t, h, http.MethodPost, "/api/runs", `{"message":"do it","session_id":"`+id+`"}`, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, want the run to start: the workspace is there, its directory is not (body %s)", w.Code, w.Body)
	}
	if runs.runs != 1 {
		t.Fatalf("runs = %d, want exactly one", runs.runs)
	}
}

// A binding whose workspace is gone is reported as missing rather than dropped:
// the session's record still names it, and a run of that session refuses to start
// over exactly that, so a client has to be able to see why instead of being shown
// a session that looks unbound and then fails when it is used.
func TestAReplayReportsAWorkspaceThatIsGone(t *testing.T) {
	h, sessions, _ := handlerWithWorkspaces(t)
	id := seedSession(t, sessions, "bound to something gone")
	const gone = "0123456789abcdef01234567"
	if err := sessions.AppendConfig(id, store.ConfigRecord{Type: store.TypeConfig, Workspace: gone}); err != nil {
		t.Fatalf("append config: %v", err)
	}

	w := request(t, h, http.MethodGet, "/api/sessions/"+id, "", false)
	var detail struct {
		Workspace *struct {
			ID      string   `json:"id"`
			Dirs    []string `json:"dirs"`
			Missing bool     `json:"missing"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if detail.Workspace == nil {
		t.Fatal("a session bound to a workspace that is gone is not bound to nothing")
	}
	if detail.Workspace.ID != gone || !detail.Workspace.Missing {
		t.Fatalf("workspace = %+v, want the id it names and missing=true", *detail.Workspace)
	}
	if detail.Workspace.Dirs == nil {
		t.Fatal("dirs must be a list even when the workspace is gone")
	}
}
