package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/cloudwego/eino/schema"
)

func TestLargerRulesReachTheModelAlongsideReferenceAndSkillContext(t *testing.T) {
	reference := strings.Repeat("reference ", 800) + "REFERENCE_END"
	rules := strings.Repeat("rule ", 5000) + "RULES_END"
	skills := strings.Repeat("procedure ", 800) + "SKILLS_END"
	reg := enabledRegistry(t,
		(&fakeCapability{id: "prior", budget: 9 * 1024, blocks: []plugin.ContextBlock{{ID: "reference", Kind: plugin.ContextReference, Text: reference}}}).plugin(),
		(&fakeCapability{id: "project", budget: 32 * 1024, blocks: []plugin.ContextBlock{{ID: "rules", Kind: plugin.ContextInstruction, Text: rules}}}).plugin(),
		(&fakeCapability{id: "procedures", budget: 9 * 1024, blocks: []plugin.ContextBlock{{ID: "skills", Kind: plugin.ContextSkill, Text: skills}}}).plugin(),
	)
	model := &captureModel{answer: "ok"}
	runner, err := NewRunner(context.Background(), model, fakeInvoker{}, &recordingReader{}, WithCapabilities(reg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), RunRequest{RunID: "rules-budget", Message: "follow project rules", Sink: &collectingSink{}}); err != nil {
		t.Fatal(err)
	}
	var system string
	for _, call := range model.all() {
		for _, message := range call {
			if message.Role == schema.System {
				system = message.Content
			}
		}
	}
	for _, text := range []string{reference, rules, skills} {
		if !strings.Contains(system, text) {
			t.Errorf("complete context block of %d bytes missing from actual model input", len(text))
		}
	}
}
