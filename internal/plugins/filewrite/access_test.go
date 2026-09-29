package filewrite

import (
	"context"
	"errors"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWaitsForApprovalBeforeReadingOrChangingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := plugin.DefaultAccessPolicy()
	ctx := plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p, Approve: func(_ context.Context, op plugin.AccessRequest) error {
		calls++
		if op.Target != target || op.ParametersDigest == "" {
			t.Fatalf("operation=%+v", op)
		}
		data, _ := os.ReadFile(target)
		if string(data) != "old" {
			t.Fatal("write happened before approval")
		}
		return plugin.ErrAccessDenied
	}}), []string{root})
	_, err := NewWriteTool("").Invoke(ctx, `{"path":"note.txt","content":"new"}`)
	if !errors.Is(err, plugin.ErrAccessDenied) || calls != 1 {
		t.Fatalf("approval calls=%d err=%v", calls, err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old" {
		t.Fatal("denied operation changed file")
	}
}
func TestSingleWriteApprovalCanGrantOneProjectPathOutsideAutomaticScope(t *testing.T) {
	root := t.TempDir()
	p := plugin.DefaultAccessPolicy()
	ctx := plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p, Approve: func(_ context.Context, op plugin.AccessRequest) error {
		if !op.ScopeApproval {
			t.Fatal("missing scope approval")
		}
		return nil
	}}), []string{root})
	if _, err := NewWriteTool("").Invoke(ctx, `{"path":"new.txt","content":"approved"}`); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(data) != "approved" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestDeniedWriteDoesNotProbeTargetPath(t *testing.T) {
	root := t.TempDir()
	p := plugin.DefaultAccessPolicy()
	p.Read = plugin.DecisionDeny
	p.Write = plugin.DecisionDeny
	ctx := plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p}), []string{root})
	_, err := NewWriteTool("").Invoke(ctx, `{"path":"missing-parent/file","content":"no"}`)
	if !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("denied call probed the filesystem: %v", err)
	}
}
