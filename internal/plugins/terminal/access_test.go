package terminal

import (
	"context"
	"errors"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"testing"
)

func TestCommandRequiresApprovalBeforeStartingSandbox(t *testing.T) {
	root := t.TempDir()
	tool := NewRunTool()
	tool.sandboxPath = "/missing-sandbox-must-not-be-started"
	ctx := plugin.WithRoots(context.Background(), []string{root})
	_, err := tool.Invoke(ctx, `{"command":"printf must-not-run"}`)
	if !errors.Is(err, plugin.ErrApprovalRequired) {
		t.Fatalf("unapproved command reached sandbox: %v", err)
	}
	p := plugin.DefaultAccessPolicy()
	p.Exec = plugin.DecisionDeny
	ctx = plugin.WithRun(ctx, plugin.RunInfo{Permissions: &p})
	_, err = tool.Invoke(ctx, `{"command":"printf must-not-run"}`)
	if !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("denied command reached sandbox: %v", err)
	}
}
