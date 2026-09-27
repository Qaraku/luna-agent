package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/workspace"
)

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
