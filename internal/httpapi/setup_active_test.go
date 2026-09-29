package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/presets"
	"github.com/Qaraku/luna-agent/internal/runconfig"
)

type heldSetupRunner struct{ ready, release chan struct{} }

func (f *heldSetupRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	req.Configured(&runconfig.Snapshot{Selection: req.Setup.Clone(), Model: "one", Capabilities: []string{"presets"}, Tools: []string{"frozen_tool"}, Resources: map[string][]string{"catalog": {"before"}}})
	close(f.ready)
	select {
	case <-f.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return (fakeRunner{}).Run(ctx, req)
}
func TestActiveSetupViewUsesFrozenReceiptAfterCatalogChanges(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("active")
	p, err := presets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(plugin.PermissionStateWrite)
	if err = reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err = reg.Enable("presets"); err != nil {
		t.Fatal(err)
	}
	runner := &heldSetupRunner{ready: make(chan struct{}), release: make(chan struct{})}
	h := New(&fakePlugins{state: everyAllowlistedTool()}, runner, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "one"}, WithCapabilities(reg))
	if w := controlsRequest(t, h, "/api/sessions/"+id+"/setup", map[string]any{"owner": "presets", "id": "general"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		request(t, h, http.MethodPost, "/api/runs", `{"session_id":"`+id+`","message":"hi"}`, true)
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
		t.Fatal("run did not start")
	}
	if err = reg.Disable("presets"); err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodGet, "/api/setup?session="+id, "", false)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var view setupView
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Active || view.Pending || view.Problem != "" || len(view.Tools) != 1 || view.Tools[0] != "frozen_tool" || len(view.Capabilities) != 1 || view.Resources["catalog"][0] != "before" {
		t.Fatalf("active snapshot=%s", w.Body)
	}
}
