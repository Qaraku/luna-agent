package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

func memoryRun(scope string, approve plugin.ApprovalFunc) context.Context {
	return plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run-one", WorkspaceID: scope, Approve: approve})
}
func TestMemoryToolProjectScopeComesFromHostAndWritesApproveFirst(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.jsonl"))
	calls := 0
	ctx := memoryRun("project-a", func(_ context.Context, r plugin.AccessRequest) error {
		calls++
		facts, _ := s.Facts()
		if len(facts) != 0 {
			t.Fatal("write preceded approval")
		}
		if !r.ScopeApproval {
			t.Fatal("private scope not approved")
		}
		return errors.New("declined")
	})
	if _, err := NewRememberTool(s).Invoke(ctx, `{"text":"project note","scope":"project"}`); err == nil {
		t.Fatal("denied note was stored")
	}
	if calls != 1 {
		t.Fatal("write approval missing")
	}
	ctx = memoryRun("project-a", func(context.Context, plugin.AccessRequest) error { return nil })
	if _, err := NewRememberTool(s).Invoke(ctx, `{"text":"project note","scope":"project"}`); err != nil {
		t.Fatal(err)
	}
	facts, _ := s.Facts()
	if len(facts) != 1 || facts[0].WorkspaceID != "project-a" || facts[0].SourceRun != "run-one" {
		t.Fatalf("facts=%+v", facts)
	}
	if _, err := NewRememberTool(s).Invoke(ctx, `{"text":"bad","scope":"project","workspace_id":"other"}`); err == nil {
		t.Fatal("model supplied workspace")
	}
}
func TestMemoryContextAndRecallFilterTheSameScopes(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := memoryRun("project-a", nil)
	p.store.RememberScoped(context.Background(), "s", "r", "global note", ScopeGlobal, "", "user")
	p.store.RememberScoped(context.Background(), "s", "r", "A note", ScopeProject, "project-a", "user")
	p.store.RememberScoped(context.Background(), "s", "r", "B note", ScopeProject, "project-b", "user")
	blocks, err := p.Contexts(ctx)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("blocks=%v %v", blocks, err)
	}
	if strings.Contains(blocks[0].Text, "B note") || !strings.Contains(blocks[0].Text, "A note") {
		t.Fatal("context leaked project")
	}
	got, err := p.recall.Invoke(ctx, `{"include_refs":true}`)
	if err != nil || strings.Contains(got, "B note") || !strings.Contains(got, "ref=") {
		t.Fatalf("recall=%q %v", got, err)
	}
	policy := plugin.DefaultAccessPolicy()
	policy.Read = plugin.DecisionDeny
	denied := plugin.WithRun(ctx, plugin.RunInfo{WorkspaceID: "project-a", Permissions: &policy})
	blocks, err = p.Contexts(denied)
	if err != nil || len(blocks) != 0 {
		t.Fatal("automatic context bypassed read denial")
	}
	if _, err = p.recall.Invoke(denied, "{}"); !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("read=%v", err)
	}
}
func TestMemoryCorrectionApprovalCannotOverwriteConcurrentChange(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.jsonl"))
	first, _ := s.RememberScoped(context.Background(), "s", "r", "old", ScopeGlobal, "", "user")
	ctx := memoryRun("", func(context.Context, plugin.AccessRequest) error {
		_, err := s.Correct(context.Background(), first.Ref(), "concurrent", ChangeOrigin{Kind: "user"})
		return err
	})
	_, err := NewUpdateTool(s).Invoke(ctx, `{"action":"correct","ref":"`+first.Ref()+`","text":"model","reason":"correction"}`)
	if !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale approved change=%v", err)
	}
	facts, _ := s.Facts()
	if len(facts) != 1 || facts[0].Text != "concurrent" {
		t.Fatal("approved stale operation overwrote newer content")
	}
}
