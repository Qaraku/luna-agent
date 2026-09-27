package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	lunas "github.com/Qaraku/luna-agent/internal/skills"
)

func TestTheDescriptorMatchesWhatThePluginExposes(t *testing.T) {
	descriptor := Descriptor()
	if descriptor.ID != PluginID || descriptor.Deployment != plugin.DeploymentBuiltin {
		t.Fatalf("descriptor=%+v", descriptor)
	}
	want := map[plugin.ContributionKind]string{
		plugin.ContributionContext: ListContextID,
		plugin.ContributionTool:    SkillToolName,
	}
	if len(descriptor.Contributions) != len(want) {
		t.Fatalf("contributions=%+v, want exactly %d", descriptor.Contributions, len(want))
	}
	for _, contribution := range descriptor.Contributions {
		if want[contribution.Kind] != contribution.ID {
			t.Fatalf("contribution=%+v, want it declared here", contribution)
		}
	}
	// No state, no routes, no panels: nothing to claim and no permission to ask
	// for.
	if len(descriptor.Claims) != 0 || len(descriptor.Permissions) != 0 {
		t.Fatalf("claims=%v permissions=%v, want none", descriptor.Claims, descriptor.Permissions)
	}
	// The registry is what enforces the two-way agreement between this
	// declaration and the implementation.
	registry := plugin.NewRegistry()
	if err := registry.Register(New(nil)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Enable(PluginID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	entry, ok := registry.Entry(PluginID)
	if !ok {
		t.Fatal("the capability is not registered")
	}
	tools := entry.Plugin.(plugin.ToolProvider).Tools()
	if len(tools) != 1 || tools[0].Name() != SkillToolName {
		t.Fatalf("tools=%v, want the one declared tool", tools)
	}
}

func TestNoSkillsContributesNoBlock(t *testing.T) {
	blocks, err := New(nil).Contexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("blocks=%+v, want none: there is nothing to list", blocks)
	}
}

func TestTheManifestIsASkillBlockNamingEverySkill(t *testing.T) {
	found := []lunas.Skill{
		{Name: "alpha", Description: "Does alpha.", Scope: lunas.ScopeUser},
		{Name: "beta", Description: "Does beta.", Scope: lunas.ScopeBuiltin},
	}
	blocks, err := New(found).Contexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks=%+v, want one", blocks)
	}
	block := blocks[0]
	if block.ID != ListContextID || block.Kind != plugin.ContextSkill {
		t.Fatalf("block=%+v, want the declared id and kind", block)
	}
	for _, want := range []string{"alpha", "beta", "(user)", "(builtin)", "Does alpha.", SkillToolName} {
		if !strings.Contains(block.Text, want) {
			t.Fatalf("block text=%q, want it to carry %q", block.Text, want)
		}
	}
	// The block stays inside the budget the descriptor asks the Kernel to
	// reserve, so the Kernel never has to cut it a second time.
	if len(block.Text) > ListContributionBudgetBytes {
		t.Fatalf("block is %d bytes, over the %d declared", len(block.Text), ListContributionBudgetBytes)
	}
	if ListBudgetBytes >= ListContributionBudgetBytes {
		t.Fatalf("the manifest budget %d leaves no room for the lead-in inside %d", ListBudgetBytes, ListContributionBudgetBytes)
	}
}

func TestAnOverfullManifestSaysSoInsideTheBlock(t *testing.T) {
	found := make([]lunas.Skill, 0, 200)
	for i := 0; i < 200; i++ {
		found = append(found, lunas.Skill{
			Name:        "skill-" + string(rune('a'+i%26)) + strings.Repeat("x", i%7),
			Description: strings.Repeat("d", 200),
			Scope:       lunas.ScopeUser,
		})
	}
	blocks, err := New(found).Contexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks=%+v, want one", blocks)
	}
	if !strings.Contains(blocks[0].Text, "hit its") || !strings.Contains(blocks[0].Text, "of 200 skills") {
		t.Fatalf("block text=%q, want the size limit stated out loud", blocks[0].Text[:200])
	}
}
