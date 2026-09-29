package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

type reloadTargetSpy struct {
	fakePlugins
	targets []string
}

func (p *reloadTargetSpy) Reload(_ context.Context, target string) error {
	p.targets = append(p.targets, target)
	return nil
}

func TestReloadAPISelectsRegisteredSources(t *testing.T) {
	p := &reloadTargetSpy{fakePlugins: fakePlugins{state: everyAllowlistedTool()}}
	h := New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210"})
	for _, body := range []map[string]any{{"tool": pluginhost.ToolReadFile}, {}} {
		response := controlsRequest(t, h, "/api/reload", body)
		if response.Code != 200 {
			t.Errorf("reload=%d: %s", response.Code, response.Body)
		}
	}
	if len(p.targets) != 2 || p.targets[0] != pluginhost.ToolReadFile || p.targets[1] != "" {
		t.Fatalf("targets=%v", p.targets)
	}
}

func TestReloadAPIRejectsCandidateAndPathInputs(t *testing.T) {
	p := &reloadTargetSpy{fakePlugins: fakePlugins{state: everyAllowlistedTool()}}
	h := New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210"})
	for _, body := range []map[string]any{{"candidate": "v2"}, {"tool": "../../bin/sh"}, {"tool": "unknown"}, {"tool": pluginhost.ToolReadFile, "path": "/tmp/foreign"}} {
		if got := controlsRequest(t, h, "/api/reload", body); got.Code != http.StatusBadRequest {
			t.Errorf("body=%v status=%d", body, got.Code)
		}
	}
	if len(p.targets) != 0 {
		t.Fatalf("invalid requests reached reloader: %v", p.targets)
	}
}
