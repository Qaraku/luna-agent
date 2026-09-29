package web

import (
	"context"
	"errors"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"testing"
)

func TestFetchDeniedBeforeAnyNetworkWork(t *testing.T) {
	p := plugin.DefaultAccessPolicy()
	p.Network = plugin.DecisionDeny
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p})
	_, err := NewFetchTool().Invoke(ctx, `{"url":"https://example.invalid/"}`)
	if !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("network request was not denied: %v", err)
	}
}
