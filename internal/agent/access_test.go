package agent

import (
	"context"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"strings"
	"testing"
)

func TestFileToolsRespectDeniedReadBeforeCallingHost(t *testing.T) {
	host := &recordingReader{}
	calls := []struct {
		name   string
		invoke func(context.Context) (string, error)
	}{
		{"read", func(ctx context.Context) (string, error) {
			return NewReadFileTool(host).InvokableRun(ctx, `{"path":"note.txt"}`)
		}},
		{"list", func(ctx context.Context) (string, error) {
			return NewListDirTool(host).InvokableRun(ctx, `{"path":"."}`)
		}},
		{"search", func(ctx context.Context) (string, error) {
			return NewSearchFilesTool(host).InvokableRun(ctx, `{"path":".","query":"text"}`)
		}},
		{"find", func(ctx context.Context) (string, error) {
			return NewFindFilesTool(host).InvokableRun(ctx, `{"path":".","pattern":"*.go"}`)
		}},
	}
	p := plugin.DefaultAccessPolicy()
	p.Read = plugin.DecisionDeny
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			sink := &collectingSink{}
			ctx := plugin.WithRun(WithRun(context.Background(), "run", sink), plugin.RunInfo{Permissions: &p})
			result, err := tc.invoke(ctx)
			if err != nil || !strings.HasPrefix(result, "the tool refused this call: ") {
				t.Fatalf("result=%q err=%v", result, err)
			}
			if len(sink.events) != 2 || sink.events[1].Type != "tool.failed" {
				t.Fatalf("events=%v", sink.events)
			}
		})
	}
	if len(host.requests)+len(host.listRequests)+len(host.searchRequests)+len(host.findRequests) != 0 {
		t.Fatal("denied read reached the file host")
	}
}
func TestReadApprovalBindsArgumentsAndStillUsesHostBoundary(t *testing.T) {
	host := &recordingReader{}
	p := plugin.DefaultAccessPolicy()
	p.Read = plugin.DecisionAsk
	approved := 0
	ctx := plugin.WithRun(WithRoots(context.Background(), []string{"/project"}), plugin.RunInfo{Permissions: &p, Approve: func(_ context.Context, op plugin.AccessRequest) error {
		approved++
		if op.Target != "note.txt" || op.ParametersDigest == "" || len(op.ReadRoots) != 1 {
			t.Fatalf("operation=%+v", op)
		}
		return nil
	}})
	if _, err := NewReadFileTool(host).InvokableRun(ctx, `{"path":"note.txt"}`); err != nil {
		t.Fatal(err)
	}
	if approved != 1 || len(host.requests) != 1 || host.requests[0].Path != "note.txt" {
		t.Fatalf("approval=%d requests=%v", approved, host.requests)
	}
}

func TestRuntimePolicyContextReflectsConfirmedFields(t *testing.T) {
	p := plugin.AccessPolicy{Read: plugin.DecisionDeny, Write: plugin.DecisionAsk, Network: plugin.DecisionAllow, Exec: plugin.DecisionDeny}
	text := runtimePermissionInstruction(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p}))
	for _, fragment := range []string{"mode=sandbox", "project read=deny", "project write=ask", "tool network=allow", "command execution=deny"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("missing %q in permission context", fragment)
		}
	}
	full := runtimePermissionInstruction(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p, ExecutionMode: plugin.ExecutionFullAccess}))
	if !strings.Contains(full, "mode=full_access") || !strings.Contains(full, "command execution=allow") {
		t.Fatal("full-access policy context does not match the effective policy")
	}
}
