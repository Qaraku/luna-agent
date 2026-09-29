package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/runconfig"
	catalog "github.com/Qaraku/luna-agent/internal/skills"
)

func TestRunSelectionControlsBothSkillManifestAndReads(t *testing.T) {
	a, _ := newSkillDir(t, "alpha", sampleSkill)
	b, _ := newSkillDir(t, "beta", strings.ReplaceAll(sampleSkill, "alpha", "beta"))
	p := New([]catalog.Skill{a, b})
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Selection: &runconfig.Selection{ID: "focus", Resources: map[string][]string{PluginID: {"alpha"}}}})
	blocks, err := p.Contexts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "alpha") || strings.Contains(blocks[0].Text, "beta") {
		t.Fatalf("manifest=%v", blocks)
	}
	if _, err = p.tool.Invoke(ctx, `{"name":"alpha"}`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.tool.Invoke(ctx, `{"name":"beta"}`); err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatalf("unselected skill read: %v", err)
	}
}

func TestFrozenResourceSetDoesNotExpandAfterSkillEnable(t *testing.T) {
	a, _ := newSkillDir(t, "alpha", sampleSkill)
	b, _ := newSkillDir(t, "beta", strings.ReplaceAll(sampleSkill, "alpha", "beta"))
	p := New([]catalog.Skill{a, b}, "beta")
	names, err := p.RunResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Resources: map[string][]string{PluginID: names}})
	p.SetDisabled("beta", false)
	if _, err = p.tool.Invoke(ctx, `{"name":"beta"}`); err == nil {
		t.Fatal("later enable widened running resource snapshot")
	}
	blocks, err := p.Contexts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blocks[0].Text, "beta") {
		t.Fatal("later enable changed running manifest")
	}
}
