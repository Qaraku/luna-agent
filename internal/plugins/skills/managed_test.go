package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

func managedArgs(t *testing.T, action string, d SkillDefinition, expected string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"action": action, "definition": d, "expected_revision": expected, "reason": "用户教过的方法"})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func managementTool(t *testing.T, p *Plugin) plugin.Tool {
	t.Helper()
	for _, tool := range p.Tools() {
		if tool.Name() == ManageToolName {
			return tool
		}
	}
	t.Fatal("management tool missing")
	return nil
}

func TestSkillSaveApprovesBeforeCreatingFiles(t *testing.T) {
	dir := t.TempDir()
	p, err := NewManaged(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := managementTool(t, p)
	d := SkillDefinition{Name: "learned", Description: "Reusable workflow", Body: "Read then apply.\n"}
	calls := 0
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run-one", Approve: func(ctx context.Context, request plugin.AccessRequest) error {
		calls++
		files, _ := os.ReadDir(dir)
		if len(files) != 0 {
			t.Fatal("write happened before approval")
		}
		if !strings.Contains(request.Preview, "+Read then apply.") || !request.ScopeApproval {
			t.Fatalf("approval lacks preview/scope: %+v", request)
		}
		return errors.New("declined")
	}})
	if _, err = tool.Invoke(ctx, managedArgs(t, "save", d, "")); err == nil {
		t.Fatal("rejected operation saved")
	}
	if calls != 1 {
		t.Fatalf("approvals=%d", calls)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("rejected write left files")
	}
	ctx = plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run-two", Approve: func(context.Context, plugin.AccessRequest) error { return nil }})
	if _, err = tool.Invoke(ctx, managedArgs(t, "save", d, "")); err != nil {
		t.Fatal(err)
	}
	saved, err := p.library.Lookup("learned", "")
	if err != nil || saved.Origin.Kind != "model" || saved.Origin.SessionID != "aaaaaaaa" || saved.Origin.RunID != "run-two" {
		t.Fatalf("origin=%+v %v", saved, err)
	}
}
func TestSkillUpdateConflictAfterApprovalDoesNotReplaceNewerContent(t *testing.T) {
	p, _ := NewManaged(t.TempDir(), nil)
	d := SkillDefinition{Name: "learned", Description: "Work", Body: "old"}
	first, err := p.library.Save(d, "", SkillOrigin{Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run", Approve: func(context.Context, plugin.AccessRequest) error {
		_, err := p.library.Save(SkillDefinition{Name: "learned", Description: "Work", Body: "concurrent"}, first.Revision, SkillOrigin{Kind: "user"})
		return err
	}})
	d.Body = "model change"
	if _, err = managementTool(t, p).Invoke(ctx, managedArgs(t, "save", d, first.Revision)); !errors.Is(err, ErrLibraryConflict) {
		t.Fatalf("stale approval=%v", err)
	}
	current, err := p.library.Definition("learned", "")
	if err != nil || current.Body != "concurrent" {
		t.Fatalf("current=%+v %v", current, err)
	}
}
func TestManagedSkillReadAndManifestUsePinnedRevision(t *testing.T) {
	p, _ := NewManaged(t.TempDir(), nil)
	a, err := p.library.Save(SkillDefinition{Name: "learned", Description: "Old method", Body: "old body"}, "", SkillOrigin{Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.library.Save(SkillDefinition{Name: "learned", Description: "New method", Body: "new body"}, a.Revision, SkillOrigin{Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Resources: map[string][]string{PluginID: {"learned"}}, ResourceRevisions: map[string]map[string]string{PluginID: {"learned": a.Revision}}})
	blocks, err := p.Contexts(ctx)
	if err != nil || len(blocks) != 1 || !strings.Contains(blocks[0].Text, "Old method") || strings.Contains(blocks[0].Text, "New method") {
		t.Fatalf("manifest=%v %v", blocks, err)
	}
	body, err := p.tool.Invoke(ctx, `{"name":"learned"}`)
	if err != nil || body != "old body" {
		t.Fatalf("body=%q %v", body, err)
	}
}
func TestSkillWriteDenyWinsBeforeLibraryProbe(t *testing.T) {
	p, _ := NewManaged(t.TempDir(), nil)
	if err := os.WriteFile(p.library.dir+"/broken.jsonl", []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := plugin.DefaultAccessPolicy()
	policy.Write = plugin.DecisionDeny
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &policy})
	_, err := managementTool(t, p).Invoke(ctx, managedArgs(t, "save", SkillDefinition{Name: "broken", Description: "Work", Body: "text"}, ""))
	if !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("deny lost priority: %v", err)
	}
}
