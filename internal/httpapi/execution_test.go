package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/store"
)

type executionRecorder struct{ modes []plugin.ExecutionMode }

func (r *executionRecorder) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	r.modes = append(r.modes, req.ExecutionMode)
	return (fakeRunner{}).Run(ctx, req)
}
func executionHandler(sessions Sessions, runner Runner) http.Handler {
	return New(&fakePlugins{state: everyAllowlistedTool()}, runner, sessions, Info{BoundHost: "127.0.0.1:43210"})
}
func executionViewForTest(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	response := request(t, h, http.MethodGet, "/api/execution?session="+id, "", false)
	if response.Code != 200 {
		t.Fatalf("view=%d %s", response.Code, response.Body)
	}
	var view map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestExecutionGrantRequiresConfirmationAndCannotComeFromSessionFile(t *testing.T) {
	sessions := newTestStore(t)
	id, err := sessions.Create("permission")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &executionRecorder{}
	h := executionHandler(sessions, recorder)
	endpoint := "/api/sessions/" + id + "/execution"
	if got := controlsRequest(t, h, endpoint, map[string]any{"mode": "full_access"}); got.Code != 400 {
		t.Fatalf("unconfirmed mode=%d", got.Code)
	}
	if err := sessions.AppendConfig(id, store.ConfigRecord{ExecutionMode: "full_access"}); err != nil {
		t.Fatal(err)
	}
	view := executionViewForTest(t, h, id)
	if view["mode"] != "sandbox" || view["needs_confirmation"] != true {
		t.Fatalf("tampered preference became authority: %v", view)
	}
	if got := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "must not run"}); got.Code != 409 {
		t.Fatalf("unapproved run=%d", got.Code)
	}
	if len(recorder.modes) != 0 {
		t.Fatal("unapproved request reached runner")
	}
	if got := controlsRequest(t, h, endpoint, map[string]any{"mode": "full_access", "confirm_full_access": true}); got.Code != 200 {
		t.Fatalf("confirmed mode=%d %s", got.Code, got.Body)
	}
	if got := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "run"}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	if len(recorder.modes) != 1 || recorder.modes[0] != plugin.ExecutionFullAccess {
		t.Fatalf("modes=%v", recorder.modes)
	}
	// 另一个 Server 相当于重启，不继承旧进程的授权。
	restarted := executionHandler(sessions, &executionRecorder{})
	if view := executionViewForTest(t, restarted, id); view["needs_confirmation"] != true {
		t.Fatalf("grant survived restart: %v", view)
	}
	if got := controlsRequest(t, h, endpoint, map[string]any{"mode": "sandbox"}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	if got := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "restricted"}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	if recorder.modes[len(recorder.modes)-1] != plugin.ExecutionSandbox {
		t.Fatal("revocation did not affect next run")
	}
}

func TestExecutionGrantDoesNotTransferToOtherSessionsOrRunParameters(t *testing.T) {
	sessions := newTestStore(t)
	a, _ := sessions.Create("A")
	b, _ := sessions.Create("B")
	recorder := &executionRecorder{}
	h := executionHandler(sessions, recorder)
	if got := controlsRequest(t, h, "/api/sessions/"+a+"/execution", map[string]any{"mode": "full_access", "confirm_full_access": true}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	if got := controlsRequest(t, h, "/api/runs", map[string]any{"session_id": b, "message": "run", "execution_mode": "full_access"}); got.Code != 400 {
		t.Fatalf("run parameter=%d", got.Code)
	}
	if got := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": b, "message": "run"}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	if len(recorder.modes) != 1 || recorder.modes[0] != plugin.ExecutionSandbox {
		t.Fatalf("cross-session grant=%v", recorder.modes)
	}
	if err := sessions.AppendConfig(b, store.ConfigRecord{ExecutionMode: "full_access"}); err != nil {
		t.Fatal(err)
	}
	if view := executionViewForTest(t, h, b); view["needs_confirmation"] != true {
		t.Fatalf("copied preference inherited another session grant: %v", view)
	}
	if got := request(t, h, http.MethodPost, "/api/sessions/"+b+"/execution", "{}", false); got.Code != 403 {
		t.Fatalf("mutation without Origin=%d", got.Code)
	}
}

type executionHoldRunner struct{ entered, release chan struct{} }

func (r executionHoldRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	close(r.entered)
	<-r.release
	return (fakeRunner{}).Run(ctx, req)
}
func TestExecutionPermissionChangesWaitForTheUserToStopAnActiveRun(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("busy")
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	h := executionHandler(sessions, executionHoldRunner{entered: entered, release: release})
	done := make(chan struct{})
	go func() {
		controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "run"})
		close(done)
	}()
	waitCompletion(t, entered)
	got := controlsRequest(t, h, "/api/sessions/"+id+"/execution", map[string]any{"mode": "full_access", "confirm_full_access": true})
	if got.Code != 409 {
		t.Fatalf("permission changed during active run: %d", got.Code)
	}
	unblock()
	waitCompletion(t, done)
	if view := executionViewForTest(t, h, id); view["mode"] != "sandbox" {
		t.Fatalf("rejected change granted permission: %v", view)
	}
}

func TestExecutionWriteFailureDoesNotGrantPermission(t *testing.T) {
	disk := newTestStore(t)
	id, _ := disk.Create("failure")
	sessions := &faultySessionConfig{Store: disk, failWrite: true}
	h := executionHandler(sessions, &executionRecorder{})
	got := controlsRequest(t, h, "/api/sessions/"+id+"/execution", map[string]any{"mode": "full_access", "confirm_full_access": true})
	if got.Code != 500 {
		t.Fatalf("failed save=%d", got.Code)
	}
	if err := disk.AppendConfig(id, store.ConfigRecord{ExecutionMode: "full_access"}); err != nil {
		t.Fatal(err)
	}
	if view := executionViewForTest(t, h, id); view["mode"] != "sandbox" || view["needs_confirmation"] != true {
		t.Fatalf("failed save left an authority grant: %v", view)
	}
}

type heldExecutionRead struct {
	*store.Store
	hold             atomic.Bool
	entered, release chan struct{}
}

func (s *heldExecutionRead) Read(id string) (store.Session, error) {
	value, err := s.Store.Read(id)
	if s.hold.CompareAndSwap(true, false) {
		close(s.entered)
		<-s.release
	}
	return value, err
}
func TestRunAdmissionDoesNotUseAPreRevocationExecutionSnapshot(t *testing.T) {
	disk := newTestStore(t)
	id, _ := disk.Create("race")
	sessions := &heldExecutionRead{Store: disk, entered: make(chan struct{}), release: make(chan struct{})}
	recorder := &executionRecorder{}
	h := executionHandler(sessions, recorder)
	endpoint := "/api/sessions/" + id + "/execution"
	if got := controlsRequest(t, h, endpoint, map[string]any{"mode": "full_access", "confirm_full_access": true}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	sessions.hold.Store(true)
	unblock := sync.OnceFunc(func() { close(sessions.release) })
	defer unblock()
	done := make(chan struct{})
	go func() {
		controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "run"})
		close(done)
	}()
	waitCompletion(t, sessions.entered)
	if got := controlsRequest(t, h, endpoint, map[string]any{"mode": "sandbox"}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	unblock()
	waitCompletion(t, done)
	if len(recorder.modes) != 1 || recorder.modes[0] != plugin.ExecutionSandbox {
		t.Fatalf("stale full-access snapshot used: %v", recorder.modes)
	}
}
