package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	skills "github.com/Qaraku/luna-agent/internal/plugins/skills"
)

type learningRunner struct{ tool plugin.Tool }

func (r learningRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	ctx = plugin.WithRun(ctx, plugin.RunInfo{RunID: req.RunID, SessionID: req.SessionID, Permissions: req.Permissions, Approve: req.Approve})
	value, err := r.tool.Invoke(ctx, `{"action":"save","definition":{"name":"learned","description":"Reusable method","body":"Read before changing."},"reason":"user taught this method"}`)
	if err != nil {
		return "tool refused: " + err.Error(), nil
	}
	return value, nil
}
func TestLearningApprovalRoundTripPrecedesLibraryPublication(t *testing.T) {
	for _, decision := range []string{"approve", "deny"} {
		t.Run(decision, func(t *testing.T) {
			dir := t.TempDir()
			p, err := skills.NewManaged(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			reg := plugin.NewRegistry(plugin.PermissionStateWrite)
			if err = reg.Register(p); err != nil {
				t.Fatal(err)
			}
			if err = reg.Enable(skills.PluginID); err != nil {
				t.Fatal(err)
			}
			var tool plugin.Tool
			for _, candidate := range p.Tools() {
				if candidate.Name() == skills.ManageToolName {
					tool = candidate
				}
			}
			if tool == nil {
				t.Fatal("learning tool missing")
			}
			sessions := newTestStore(t)
			id, _ := sessions.Create("learning")
			h := New(&fakePlugins{state: everyAllowlistedTool()}, learningRunner{tool}, sessions, Info{BoundHost: "127.0.0.1:43210"}, WithCapabilities(reg), WithRunTimeout(2*time.Second))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "learn"})
			}()
			deadline := time.Now().Add(time.Second)
			var pending approvalView
			for time.Now().Before(deadline) {
				response := request(t, h, http.MethodGet, "/api/approvals?session="+id, "", false)
				var body struct {
					Approvals []approvalView `json:"approvals"`
				}
				if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Approvals) > 0 {
					pending = body.Approvals[0]
					break
				}
				time.Sleep(time.Millisecond)
			}
			if pending.ID == "" {
				t.Fatal("no learning approval")
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("library changed before approval")
			}
			if w := controlsRequest(t, h, "/api/approvals/"+pending.ID, map[string]string{"session_id": id, "run_id": pending.RunID, "decision": decision}); w.Code != 200 {
				t.Fatal(w.Body)
			}
			select {
			case w := <-done:
				if w.Code != 200 {
					t.Fatal(w.Body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("learning run did not finish")
			}
			statuses, err := p.Skills()
			if err != nil {
				t.Fatal(err)
			}
			if (len(statuses) == 1) != (decision == "approve") {
				t.Fatalf("decision=%s statuses=%+v", decision, statuses)
			}
		})
	}
}
func TestSkillLibraryMutationRoutesRequireOriginAndRejectForgedOrigin(t *testing.T) {
	dir := t.TempDir()
	p, err := skills.NewManaged(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(plugin.PermissionStateWrite)
	if err = reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err = reg.Enable(skills.PluginID); err != nil {
		t.Fatal(err)
	}
	h := handlerWithStore(t, fakeRunner{}, newTestStore(t), WithCapabilities(reg))
	for _, path := range []string{"/api/skill-library/preview", "/api/skill-library/save", "/api/skill-library/restore"} {
		if w := request(t, h, http.MethodPost, path, "{}", false); w.Code != 403 {
			t.Fatalf("unguarded %s=%d", path, w.Code)
		}
	}
	body := `{"definition":{"name":"safe","description":"Work","body":"A method"},"expected_revision":"","reason":"manual"}`
	w := request(t, h, http.MethodPost, "/api/skill-library/preview", body, true)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("preview wrote files")
	}
	forged := `{"definition":{"name":"safe","description":"Work","body":"A method"},"origin":{"kind":"model","session_id":"forged"}}`
	if w = request(t, h, http.MethodPost, "/api/skill-library/save", forged, true); w.Code != 400 {
		t.Fatal("forged source accepted", w.Body)
	}
	if w = request(t, h, http.MethodPost, "/api/skill-library/save", body, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	view := request(t, h, http.MethodGet, "/api/skill-library/detail?name=safe", "", false)
	if view.Code != 200 {
		t.Fatal(view.Body)
	}
	var detail struct{ Entry skills.SkillRevision }
	if err = json.Unmarshal(view.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Entry.Origin.Kind != "user" || detail.Entry.Origin.SessionID != "" {
		t.Fatalf("origin=%+v", detail.Entry.Origin)
	}
}
