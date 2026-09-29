package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/presets"
	"github.com/Qaraku/luna-agent/internal/runconfig"
	"github.com/Qaraku/luna-agent/internal/store"
)

type setupRecorder struct{ request agent.RunRequest }

func (f *setupRecorder) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	f.request = req
	return (fakeRunner{}).Run(ctx, req)
}
func setupHandler(t *testing.T) (http.Handler, *store.Store, *presets.Plugin, *plugin.Registry, *setupRecorder) {
	t.Helper()
	p, err := presets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(plugin.PermissionStateWrite)
	if err = reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err = reg.Enable(presets.PluginID); err != nil {
		t.Fatal(err)
	}
	sessions := newTestStore(t)
	runner := &setupRecorder{}
	h := New(&fakePlugins{state: everyAllowlistedTool()}, runner, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "one", ReasoningEffort: "medium", Models: []ModelRef{{Name: "one"}, {Name: "two"}}}, WithCapabilities(reg))
	return h, sessions, p, reg, runner
}
func saveTestPreset(t *testing.T, h http.Handler, instructions, revision string) presets.Entry {
	t.Helper()
	effort := "max"
	body := map[string]any{"definition": runconfig.Selection{ID: "personal", Title: "私人", Instructions: instructions, Model: "two", ReasoningEffort: &effort, Capabilities: []string{}}, "expected_revision": revision}
	w := controlsRequest(t, h, "/api/presets/save", body)
	if w.Code != 200 {
		t.Fatalf("save=%d %s", w.Code, w.Body)
	}
	var entry presets.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}
func TestSessionSetupPinsRevisionAndPreservesPermissions(t *testing.T) {
	h, sessions, _, _, runner := setupHandler(t)
	first := saveTestPreset(t, h, "first method", "")
	id, _ := sessions.Create("one")
	other, _ := sessions.Create("other")
	result := controlsRequest(t, h, "/api/sessions/"+id+"/setup", map[string]any{"owner": "presets", "id": "personal"})
	if result.Code != 200 {
		t.Fatalf("bind=%d %s", result.Code, result.Body)
	}
	saveTestPreset(t, h, "new method", first.Revision)
	ran := controlsRequest(t, h, "/api/runs", map[string]any{"session_id": id, "message": "hello"})
	if ran.Code != 200 {
		t.Fatalf("run=%d %s", ran.Code, ran.Body)
	}
	req := runner.request
	if req.Setup == nil || req.Setup.Revision != first.Revision || req.Setup.Instructions != "first method" {
		t.Fatalf("unpinned setup: %+v", req.Setup)
	}
	if req.Model != "two" || req.ReasoningEffort == nil || *req.ReasoningEffort != "max" {
		t.Fatalf("preset defaults not applied: %+v", req)
	}
	if req.Permissions == nil || *req.Permissions != plugin.DefaultAccessPolicy() {
		t.Fatal("preset granted execution permissions")
	}
	untouched, err := sessions.Read(other)
	if err != nil || untouched.Config != nil {
		t.Fatal("another session changed")
	}
	selected, err := sessions.Read(id)
	if err != nil || selected.Config.Permissions != nil || selected.Config.ExecutionMode != "" {
		t.Fatal("binding wrote authorization")
	}
}
func TestSetupDefaultsYieldToExplicitChoicesAndReturnOnReset(t *testing.T) {
	h, sessions, _, _, _ := setupHandler(t)
	saveTestPreset(t, h, "method", "")
	id, _ := sessions.Create("one")
	if w := controlsRequest(t, h, "/api/sessions/"+id+"/setup", map[string]any{"owner": "presets", "id": "personal"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	for _, tc := range []struct {
		modelBody, effortBody         map[string]any
		wantModel, wantEffort, origin string
	}{
		{map[string]any{"model": "one"}, map[string]any{"reasoning_effort": "none"}, "one", "none", "session"},
		{map[string]any{"reset": true}, map[string]any{"reset": true}, "two", "max", "setup"},
	} {
		w := controlsRequest(t, h, "/api/sessions/"+id+"/model", tc.modelBody)
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		var choice map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &choice)
		if choice["model"] != tc.wantModel || choice["origin"] != tc.origin {
			t.Fatalf("model response=%s", w.Body)
		}
		w = controlsRequest(t, h, "/api/sessions/"+id+"/reasoning", tc.effortBody)
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		var choiceReason struct{ Current currentReasoning }
		_ = json.Unmarshal(w.Body.Bytes(), &choiceReason)
		if choiceReason.Current.Effort != tc.wantEffort || choiceReason.Current.Origin != tc.origin {
			t.Fatalf("reasoning response=%s", w.Body)
		}
		view := decodeModels(t, request(t, h, http.MethodGet, "/api/models?session="+id, "", false).Body.Bytes())
		if view.Current.Name != tc.wantModel || view.Current.Origin != tc.origin {
			t.Fatalf("model view=%+v", view)
		}
	}
}
func TestSetupAndPresetWritesRequireOriginAndRejectAuthorityFields(t *testing.T) {
	h, sessions, _, _, _ := setupHandler(t)
	id, _ := sessions.Create("one")
	for _, path := range []string{"/api/presets/save", "/api/presets/archive", "/api/presets/restore", "/api/sessions/" + id + "/setup"} {
		if w := request(t, h, http.MethodPost, path, "{}", false); w.Code != 403 {
			t.Fatalf("unguarded %s=%d", path, w.Code)
		}
	}
	for _, body := range []string{
		`{"owner":"presets","id":"general","permissions":{"exec":"allow"}}`,
		`{"owner":"presets","id":"general","reset":true}`,
		`{"owner":"presets","id":"general","instructions":"injected"}`,
		`null`,
	} {
		w := request(t, h, http.MethodPost, "/api/sessions/"+id+"/setup", body, true)
		if w.Code != 400 {
			t.Fatalf("body=%s status=%d %s", body, w.Code, w.Body)
		}
	}
	w := request(t, h, http.MethodPost, "/api/presets/save", `{"definition":{"id":"evil","permissions":{"exec":"allow"}}}`, true)
	if w.Code != 400 {
		t.Fatal("preset accepted authorization fields", w.Body)
	}
	session, _ := sessions.Read(id)
	if session.Config != nil {
		t.Fatal("rejected setup changed session")
	}
}
func TestDisabledSetupOwnerCannotBeAppliedOrRun(t *testing.T) {
	h, sessions, _, reg, _ := setupHandler(t)
	id, _ := sessions.Create("one")
	if w := controlsRequest(t, h, "/api/sessions/"+id+"/setup", map[string]any{"owner": "presets", "id": "general"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if err := reg.Disable("presets"); err != nil {
		t.Fatal(err)
	}
	if w := controlsRequest(t, h, "/api/runs", map[string]any{"session_id": id, "message": "hello"}); w.Code != 409 {
		t.Fatalf("disabled owner run=%d %s", w.Code, w.Body)
	}
	if w := controlsRequest(t, h, "/api/sessions/"+id+"/setup", map[string]any{"reset": true}); w.Code != 200 {
		t.Fatalf("cannot remove disabled setup: %d %s", w.Code, w.Body)
	}
}
