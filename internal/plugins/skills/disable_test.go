package skills

import (
	"context"
	"strings"
	"testing"

	lunas "github.com/Qaraku/luna-agent/internal/skills"
)

// twoSkills is one built and one user skill, so a test can name either.
func twoSkills() []lunas.Skill {
	return []lunas.Skill{
		{Name: "alpha", Description: "Does alpha.", Scope: lunas.ScopeUser},
		{Name: "beta", Description: "Does beta.", Scope: lunas.ScopeBuiltin},
	}
}

func TestADisabledSkillIsNotInTheManifest(t *testing.T) {
	plugin := New(twoSkills(), "alpha")
	blocks, err := plugin.Contexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks=%+v, want the one block for the skill in service", blocks)
	}
	text := blocks[0].Text
	if strings.Contains(text, "alpha") || strings.Contains(text, "Does alpha.") {
		t.Fatalf("manifest=%q, want the disabled skill absent", text)
	}
	if !strings.Contains(text, "beta") {
		t.Fatalf("manifest=%q, want the enabled skill present", text)
	}

	// Turning that one off too leaves nothing to list, which is no block at
	// all rather than a header with nothing under it. It takes effect on the
	// next read, with no rebuild in between.
	if !plugin.SetDisabled("beta", true) {
		t.Fatal("beta was discovered but SetDisabled said it is not there")
	}
	blocks, err = plugin.Contexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("blocks=%+v, want none: every skill is turned off", blocks)
	}
}

func TestATurnedOffSkillIsRefusedByTheTool(t *testing.T) {
	skill, _ := newSkillDir(t, "alpha", sampleSkill)
	plugin := New([]lunas.Skill{skill}, "alpha")

	_, err := invoke(t, plugin.tool, `{"name":"alpha"}`)
	if err == nil {
		t.Fatal("a skill the user turned off was read back")
	}
	if !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("err=%v, want the refusal to name the reason", err)
	}
	// The refusal says it is off in settings, not that no such skill exists:
	// those are different facts and the model acts differently on each.
	if strings.Contains(err.Error(), "no skill named") {
		t.Fatalf("err=%v, want the settings reason, not an absent skill", err)
	}

	// Nothing in the set changed: turning it back on makes it readable again,
	// which is what proves the refusal was the selection and not a lost skill.
	if !plugin.SetDisabled("alpha", false) {
		t.Fatal("alpha is discovered, so turning it back on must be accepted")
	}
	body, err := invoke(t, plugin.tool, `{"name":"alpha"}`)
	if err != nil {
		t.Fatalf("the skill is back in service: %v", err)
	}
	if !strings.Contains(body, "Step one.") {
		t.Fatalf("body=%q, want the skill's own text", body)
	}
}

func TestAnUnknownNameIsNotStoredInTheSelection(t *testing.T) {
	plugin := New(twoSkills())
	if plugin.SetDisabled("ghost", true) {
		t.Fatal("a name that was never discovered was accepted")
	}
	if plugin.SetDisabled("  ", true) {
		t.Fatal("a blank name was accepted")
	}
	names, err := plugin.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("skills=%+v, want both untouched", names)
	}
	for _, status := range names {
		if !status.Enabled {
			t.Fatalf("%s is off after no accepted change", status.Name)
		}
	}
}

func TestSkillsReportsTheStateTheInterfaceShows(t *testing.T) {
	plugin := New(twoSkills(), "beta")
	statuses, err := plugin.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses=%+v, want both discovered skills", statuses)
	}
	alpha, beta := statuses[0], statuses[1]
	if !alpha.Enabled || alpha.DisabledReason != "" {
		t.Fatalf("alpha=%+v, want it on with no reason", alpha)
	}
	if beta.Enabled || beta.DisabledReason != DisabledReasonSetting {
		t.Fatalf("beta=%+v, want it off with the settings reason", beta)
	}
	if beta.Description != "Does beta." {
		t.Fatalf("beta=%+v, want the discovered description", beta)
	}
}
