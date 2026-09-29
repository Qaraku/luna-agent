package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/store"
)

func TestSessionMetadataRenameArchiveRestoreAndFilters(t *testing.T) {
	h, sessions := modelsHandler(t)
	first, _ := sessions.Create("Original")
	second, _ := sessions.Create("Other")
	if err := sessions.AppendConfig(first, store.ConfigRecord{Model: "two", Workspace: "project-one"}); err != nil {
		t.Fatal(err)
	}
	rename := controlsRequest(t, h, "/api/sessions/"+first+"/metadata", map[string]any{"title": "修改后的标题"})
	if rename.Code != 200 {
		t.Fatalf("rename=%d %s", rename.Code, rename.Body)
	}
	got, _ := sessions.Read(first)
	if got.Title != "修改后的标题" || got.Config.Model != "two" || got.Config.Workspace != "project-one" {
		t.Fatalf("metadata lost configuration: %+v", got)
	}
	view := request(t, h, http.MethodGet, "/api/sessions?q=修改&workspace=project-one", "", false)
	var payload struct {
		Sessions []sessionSummary
		Total    int
	}
	if err := json.Unmarshal(view.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 1 || payload.Sessions[0].ID != first {
		t.Fatalf("filtered=%s", view.Body)
	}
	if w := controlsRequest(t, h, "/api/sessions/"+first+"/metadata", map[string]any{"archived": true}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	view = request(t, h, http.MethodGet, "/api/sessions", "", false)
	_ = json.Unmarshal(view.Body.Bytes(), &payload)
	if len(payload.Sessions) != 1 || payload.Sessions[0].ID != second {
		t.Fatalf("default archive filter=%s", view.Body)
	}
	view = request(t, h, http.MethodGet, "/api/sessions?archived=only", "", false)
	_ = json.Unmarshal(view.Body.Bytes(), &payload)
	if len(payload.Sessions) != 1 || !payload.Sessions[0].Archived {
		t.Fatalf("archive list=%s", view.Body)
	}
	if w := controlsRequest(t, h, "/api/sessions/"+first+"/metadata", map[string]any{"archived": false}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	got, _ = sessions.Read(first)
	if got.Archived || got.Title != "修改后的标题" || got.Config.Model != "two" {
		t.Fatal("restore lost metadata")
	}
}
func TestSessionMetadataRejectsBadInputsAndRequiresOrigin(t *testing.T) {
	h, sessions := modelsHandler(t)
	id, _ := sessions.Create("safe")
	path := "/api/sessions/" + id + "/metadata"
	if w := request(t, h, http.MethodPost, path, `{"title":"changed"}`, false); w.Code != 403 {
		t.Fatalf("origin=%d", w.Code)
	}
	for _, body := range []string{`{}`, `null`, `{"title":" "}`, `{"archived":"yes"}`, `{"title":"new","permissions":{"write":"allow"}}`} {
		if w := request(t, h, http.MethodPost, path, body, true); w.Code != 400 {
			t.Fatalf("body=%s status=%d %s", body, w.Code, w.Body)
		}
	}
	got, _ := sessions.Read(id)
	if got.Title != "safe" || got.Config != nil {
		t.Fatal("invalid metadata wrote records")
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?offset=-1", "?archived=maybe"} {
		if w := request(t, h, http.MethodGet, "/api/sessions"+query, "", false); w.Code != 400 {
			t.Fatalf("query=%s status=%d", query, w.Code)
		}
	}
}
func TestArchivedSessionCannotStartAnotherRun(t *testing.T) {
	h, sessions := modelsHandler(t)
	id, _ := sessions.Create("archive")
	if err := sessions.AppendConfig(id, store.ConfigRecord{Archived: true}); err != nil {
		t.Fatal(err)
	}
	w := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "should not run"})
	if w.Code != 409 {
		t.Fatalf("archived run=%d %s", w.Code, w.Body)
	}
}
func TestSessionListPaginatesWithoutHidingTheRemainingCount(t *testing.T) {
	h, sessions := modelsHandler(t)
	for i := 0; i < 3; i++ {
		sessions.Create("title")
	}
	w := request(t, h, http.MethodGet, "/api/sessions?limit=2", "", false)
	var view struct {
		Sessions []sessionSummary
		Total    int
		Next     *int `json:"next_offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Sessions) != 2 || view.Total != 3 || view.Next == nil || *view.Next != 2 {
		t.Fatalf("page=%s", w.Body)
	}
}

func TestWorkspaceIdentityReachesTheAdmittedRun(t *testing.T) {
	runner := &setupRecorder{}
	h, sessions, workspaces := serverWithWorkspaces(t, runner)
	item, err := workspaces.Create("Scope", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := sessions.Create("scope")
	if err = sessions.AppendConfig(id, store.ConfigRecord{Workspace: item.ID}); err != nil {
		t.Fatal(err)
	}
	if w := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "scope"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if runner.request.WorkspaceID != item.ID {
		t.Fatalf("host scope missing: %q", runner.request.WorkspaceID)
	}
}

func TestActiveSessionCanBeRenamedButNotArchived(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("running")
	runner := &heldSetupRunner{ready: make(chan struct{}), release: make(chan struct{})}
	h := handlerWithStore(t, runner, sessions)
	done := make(chan struct{})
	go func() {
		defer close(done)
		controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "hi"})
	}()
	defer func() {
		close(runner.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("run did not stop")
		}
	}()
	select {
	case <-runner.ready:
	case <-time.After(time.Second):
		t.Fatal("run did not begin")
	}
	if w := controlsRequest(t, h, "/api/sessions/"+id+"/metadata", map[string]any{"archived": true}); w.Code != 409 {
		t.Fatalf("active archive=%d %s", w.Code, w.Body)
	}
	if w := controlsRequest(t, h, "/api/sessions/"+id+"/metadata", map[string]any{"title": "Renamed while running"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	session, _ := sessions.Read(id)
	if session.Archived || session.Title != "Renamed while running" {
		t.Fatal("metadata state mismatch")
	}
}
