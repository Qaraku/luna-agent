package httpapi

import (
	"context"
	"encoding/json"
	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/filewrite"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type approvalWriteRunner struct{}

func (approvalWriteRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.Event{Type: "run.started", Data: map[string]string{"run_id": req.RunID, "session_id": req.SessionID}})
	ctx = plugin.WithRun(plugin.WithRoots(ctx, req.Roots), plugin.RunInfo{RunID: req.RunID, SessionID: req.SessionID, ExecutionMode: req.ExecutionMode, Permissions: req.Permissions, Approve: req.Approve})
	result, err := filewrite.NewWriteTool("").Invoke(ctx, `{"path":"approved.txt","content":"only after approval"}`)
	if err != nil {
		return "tool refused: " + err.Error(), nil
	}
	return result, nil
}
func TestHTTPApprovalRoundTripPrecedesRealFileWrite(t *testing.T) {
	for _, decision := range []string{"approve", "deny"} {
		t.Run(decision, func(t *testing.T) {
			root := t.TempDir()
			sessions := newTestStore(t)
			id, _ := sessions.Create("test")
			h := New(&fakePlugins{state: everyAllowlistedTool()}, approvalWriteRunner{}, sessions, Info{BoundHost: "127.0.0.1:43210"}, WithFallbackRoot(root), WithRunTimeout(2*time.Second))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "write fixture"})
			}()
			deadline := time.Now().Add(time.Second)
			var pending approvalView
			for time.Now().Before(deadline) {
				r := request(t, h, http.MethodGet, "/api/approvals?session="+id, "", false)
				var payload struct {
					Approvals []approvalView `json:"approvals"`
				}
				if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload.Approvals) > 0 {
					pending = payload.Approvals[0]
					break
				}
				time.Sleep(time.Millisecond)
			}
			if pending.ID == "" {
				t.Fatal("run did not publish an approval")
			}
			if _, err := os.Stat(filepath.Join(root, "approved.txt")); !os.IsNotExist(err) {
				t.Fatal("file exists before approval")
			}
			got := controlsRequest(t, h, "/api/approvals/"+pending.ID, map[string]string{"run_id": pending.RunID, "session_id": id, "decision": decision})
			if got.Code != 200 {
				t.Fatalf("decision=%d %s", got.Code, got.Body)
			}
			select {
			case response := <-done:
				if response.Code != 200 {
					t.Fatalf("run=%d %s", response.Code, response.Body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("run did not complete")
			}
			data, err := os.ReadFile(filepath.Join(root, "approved.txt"))
			if decision == "approve" && (err != nil || string(data) != "only after approval") {
				t.Fatalf("approved file=%q err=%v", data, err)
			}
			if decision == "deny" && !os.IsNotExist(err) {
				t.Fatal("denied operation wrote a file")
			}
		})
	}
}
