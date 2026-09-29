package httpapi

import (
	"context"
	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/workspace"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type scopePausedRunner struct {
	started chan agent.RunRequest
	release chan struct{}
}

func (r *scopePausedRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	r.started <- req
	select {
	case <-r.release:
		return (fakeRunner{}).Run(ctx, req)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func TestExecutionViewKeepsActiveScopeUntilRunFinishes(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("scope")
	projects, err := workspace.Open(filepath.Join(t.TempDir(), workspace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := projects.Create("first", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := projects.Create("second", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.AppendConfig(id, store.ConfigRecord{Workspace: a.ID}); err != nil {
		t.Fatal(err)
	}
	runner := &scopePausedRunner{started: make(chan agent.RunRequest, 1), release: make(chan struct{})}
	h := handlerWithStore(t, runner, sessions, WithWorkspaces(projects), WithRunTimeout(2*time.Second))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "scope"})
	}()
	select {
	case req := <-runner.started:
		if len(req.Roots) != 1 || req.Roots[0] != a.Dirs[0] {
			t.Fatalf("run roots=%v", req.Roots)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not start")
	}
	if err := sessions.AppendConfig(id, store.ConfigRecord{Workspace: b.ID, ExecutionMode: "full_access"}); err != nil {
		t.Fatal(err)
	}
	active := permissionsView(t, h, id)
	if len(active.Scopes.ProjectDirs) != 1 || active.Scopes.ProjectDirs[0] != a.Dirs[0] || active.NeedsConfirmation {
		t.Fatalf("active snapshot changed: %+v", active)
	}
	close(runner.release)
	select {
	case response := <-done:
		if response.Code != http.StatusOK {
			t.Fatal(response.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
	next := permissionsView(t, h, id)
	if !next.NeedsConfirmation || len(next.Scopes.ProjectDirs) != 1 || next.Scopes.ProjectDirs[0] != b.Dirs[0] {
		t.Fatalf("next preference=%+v", next)
	}
}
