package httpapi

import (
	"context"
	"encoding/json"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/runtimewidgets"
	"net/http"
	"strings"
	"testing"
)

func TestRuntimeWidgetToolAndSourceShareCapabilityLifecycle(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("runtime")
	reg := plugin.NewRegistry()
	p := runtimewidgets.New()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enable(runtimewidgets.PluginID); err != nil {
		t.Fatal(err)
	}
	h := handlerWithStore(t, fakeRunner{}, sessions, WithCapabilities(reg))
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{RunID: "run", SessionID: id})
	if _, err := p.Tools()[0].Invoke(ctx, `{"action":"upsert","id":"build","title":"Build","kind":"status","text":"reported by model"}`); err != nil {
		t.Fatal(err)
	}
	response := request(t, h, http.MethodGet, runtimewidgets.SourcePath+"?session="+id, "", false)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "reported by model") {
		t.Fatalf("source=%d %s", response.Code, response.Body)
	}
	state := request(t, h, http.MethodGet, "/api/state", "", false)
	var payload struct {
		Capabilities []capabilityView `json:"capabilities"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Capabilities) != 1 || len(payload.Capabilities[0].Widgets) != 1 || payload.Capabilities[0].Widgets[0].Source != runtimewidgets.SourcePath {
		t.Fatalf("state=%s", state.Body)
	}
	if got := controlsRequest(t, h, "/api/plugins/"+runtimewidgets.PluginID+"/disable", map[string]any{}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	if got := request(t, h, http.MethodGet, runtimewidgets.SourcePath+"?session="+id, "", false); got.Code != 404 {
		t.Fatalf("disabled source=%d", got.Code)
	}
	if err := reg.Enable(runtimewidgets.PluginID); err != nil {
		t.Fatal(err)
	}
	if got := request(t, h, http.MethodGet, runtimewidgets.SourcePath+"?session="+id, "", false); !strings.Contains(got.Body.String(), "reported by model") {
		t.Fatal("disable deleted capability data")
	}
}
