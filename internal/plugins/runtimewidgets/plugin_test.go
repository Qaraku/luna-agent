package runtimewidgets

import (
	"context"
	"encoding/json"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"strings"
	"testing"
)

func widgetCtx(id string) context.Context {
	return plugin.WithRun(context.Background(), plugin.RunInfo{RunID: "run", SessionID: id})
}
func invokeWidget(t *testing.T, p *Plugin, session string, args map[string]any) (string, error) {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return p.Tools()[0].Invoke(widgetCtx(session), string(data))
}
func TestModelWidgetsAreSessionBoundAndLayoutOnlyUpdatesPreserveData(t *testing.T) {
	p := New()
	reg := plugin.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enable(PluginID); err != nil {
		t.Fatal(err)
	}
	_, err := invokeWidget(t, p, "aaaaaaaa", map[string]any{"action": "upsert", "id": "build", "title": "构建状态", "kind": "progress", "text": "编译中", "progress": 40})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWidget(t, p, "aaaaaaaa", map[string]any{"action": "upsert", "id": "build", "layout": map[string]any{"placement": "left", "visible": false}})
	if err != nil {
		t.Fatal(err)
	}
	own := p.snapshot("aaaaaaaa")
	if len(own) != 1 || own[0].Data.Text != "编译中" || *own[0].Data.Progress != 40 || own[0].Layout.Placement != "left" || *own[0].Layout.Visible {
		t.Fatalf("widget=%+v", own)
	}
	if len(p.snapshot("bbbbbbbb")) != 0 {
		t.Fatal("widget leaked to another session")
	}
	if _, err = invokeWidget(t, p, "aaaaaaaa", map[string]any{"action": "remove", "id": "build"}); err != nil {
		t.Fatal(err)
	}
	if len(p.snapshot("aaaaaaaa")) != 0 {
		t.Fatal("removed widget remains")
	}
}
func TestModelWidgetRejectsCodePermissionAndUnboundedPayloads(t *testing.T) {
	p := New()
	for _, args := range []map[string]any{
		{"action": "upsert", "id": "x", "title": "x", "code": "alert(1)"},
		{"action": "upsert", "id": "x", "title": "x", "session_id": "bbbbbbbb"},
		{"action": "upsert", "id": "x", "title": "x", "permissions": map[string]string{"exec": "allow"}},
		{"action": "upsert", "id": "../x", "title": "x"},
		{"action": "upsert", "id": "x", "title": "x", "text": strings.Repeat("x", 4097)},
		{"action": "upsert", "id": "x", "title": "x", "kind": "html"},
		{"action": "upsert", "id": "x", "title": "x", "progress": 101},
		{"action": "upsert", "id": "x", "title": "x", "layout": map[string]any{"x": 2}},
	} {
		if _, err := invokeWidget(t, p, "aaaaaaaa", args); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
	if len(p.snapshot("aaaaaaaa")) != 0 {
		t.Fatal("invalid request mutated widgets")
	}
	if _, err := invokeWidget(t, p, "", map[string]any{"action": "list"}); err == nil {
		t.Fatal("missing session accepted")
	}
}
func TestModelWidgetCountAndUnknownProgressAreExplicit(t *testing.T) {
	p := New()
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		if _, err := invokeWidget(t, p, "aaaaaaaa", map[string]any{"action": "upsert", "id": id, "title": id, "kind": "progress"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := invokeWidget(t, p, "aaaaaaaa", map[string]any{"action": "upsert", "id": "overflow", "title": "overflow"}); err == nil {
		t.Fatal("unbounded widget count")
	}
	if p.snapshot("aaaaaaaa")[0].Data.Progress != nil {
		t.Fatal("missing progress became fake zero")
	}
}
