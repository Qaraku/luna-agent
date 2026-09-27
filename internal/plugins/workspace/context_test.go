package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// The capability contributes exactly one block, of the reference kind, saying
// which project this session is in and what the paths are relative to. Anything
// the Kernel adds around it (the source label, the framing, the budget) is the
// Kernel's, so it is deliberately absent from this text.
func TestContextsRendersOneReferenceBlockNamingTheProject(t *testing.T) {
	p := newPlugin(t, projectRoot(t))
	blocks, err := p.Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks=%+v, want exactly one", blocks)
	}
	block := blocks[0]
	if block.ID != ProjectContextID || block.Kind != plugin.ContextReference {
		t.Fatalf("block=%+v, want the declared reference contribution", block)
	}
	for _, want := range []string{projectBlockHeader, "Project: luna-agent", "read root the file tools are bounded to", "relative to it"} {
		if !strings.Contains(block.Text, want) {
			t.Fatalf("block does not contain %q:\n%s", want, block.Text)
		}
	}
	if strings.HasPrefix(block.Text, "\n") || strings.HasSuffix(block.Text, "\n\n") {
		t.Fatalf("the block carries its own spacing: %q", block.Text)
	}
}

// The model-visible text never names a host path: another machine's layout must
// not become part of every run, and it is not information the model needs. The
// same convention governs the file tool's description.
func TestTheBlockCarriesNoHostPath(t *testing.T) {
	root := projectRoot(t)
	text := newPlugin(t, root).Render()
	for _, forbidden := range []string{root, strings.TrimSuffix(root, "luna-agent")} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the block carries the host path %q:\n%s", forbidden, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "/") {
			t.Fatalf("the block has an absolute path line %q:\n%s", line, text)
		}
	}
}

// An identity statement carries identity, not metadata: no serving generation,
// version, process id or file path of a plugin process can appear here, because
// this capability has no process and holds no such field.
func TestTheBlockCarriesNoProcessIdentity(t *testing.T) {
	text := newPlugin(t, projectRoot(t)).Render()
	for _, forbidden := range []string{"pid", "process", "generation", "version", "proc"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("the block mentions %q:\n%s", forbidden, text)
		}
	}
}

// The block must fit the budget the descriptor asks the Kernel for: an identity
// sentence cut at the last complete line would tell the model something the
// capability never said.
func TestTheBlockFitsTheBudgetDeclaredForIt(t *testing.T) {
	declared := 0
	for _, c := range Descriptor().Contributions {
		if c.Kind == plugin.ContributionContext && c.ID == ProjectContextID {
			declared = c.BudgetBytes
		}
	}
	if declared != ProjectBudgetBytes {
		t.Fatalf("declared budget=%d, want %d", declared, ProjectBudgetBytes)
	}
	text := newPlugin(t, projectRoot(t)).Render()
	if len(text) == 0 || len(text) > declared {
		t.Fatalf("block size=%d, want 1..%d bytes", len(text), declared)
	}
}

// Two context reads inside one run render the same block: identity is fixed
// when the capability is constructed, so a capability that cannot change
// between reads cannot silently contribute nothing on the second one.
func TestTwoReadsOfTheSameRunRenderTheSameBlock(t *testing.T) {
	p := newPlugin(t, projectRoot(t))
	first, err := p.Contexts(context.Background())
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := p.Contexts(context.Background())
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("first=%+v second=%+v, want the same single block", first, second)
	}
}

// When the project states rules, the capability contributes a second block of
// the instruction kind, carrying the rule text the composition root read. The
// project identity stays the reference block it always was.
func TestContextsAddsAnInstructionBlockWhenTheProjectStatesRules(t *testing.T) {
	const rule = "Commit messages are written in English."
	blocks, err := newPluginWithRules(t, projectRoot(t), rule).Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks=%+v, want the identity and the rules blocks", blocks)
	}
	if blocks[0].ID != ProjectContextID || blocks[0].Kind != plugin.ContextReference {
		t.Fatalf("blocks[0]=%+v, want the reference identity block", blocks[0])
	}
	rules := blocks[1]
	if rules.ID != RulesContextID || rules.Kind != plugin.ContextInstruction {
		t.Fatalf("blocks[1]=%+v, want an instruction contribution with id %q", rules, RulesContextID)
	}
	for _, want := range []string{rulesBlockHeader, rule} {
		if !strings.Contains(rules.Text, want) {
			t.Fatalf("the rules block does not contain %q:\n%s", want, rules.Text)
		}
	}
}

// A project that states no rules contributes no instruction block: an empty rule
// block would tell the model "follow this" and then say nothing. Whitespace is
// the same statement, so it is treated the same way.
func TestContextsContributesNoRulesBlockWithoutRuleText(t *testing.T) {
	for _, rules := range []string{"", "   ", "\n	\n"} {
		blocks, err := newPluginWithRules(t, projectRoot(t), rules).Contexts(context.Background())
		if err != nil {
			t.Fatalf("Contexts(%q): %v", rules, err)
		}
		if len(blocks) != 1 {
			t.Fatalf("rules=%q produced %+v, want the identity block alone", rules, blocks)
		}
		for _, block := range blocks {
			if block.Kind == plugin.ContextInstruction {
				t.Fatalf("rules=%q produced an instruction block: %+v", rules, block)
			}
		}
	}
}

// The capability never cuts a rule set to fit: a set that does not fit the
// budget it declared for it is dropped, because a rule that stops mid-sentence
// is a rule the project never wrote. The boundary is exact, so the case that
// fits is not refused as well.
func TestAnOverLongRuleSetIsRefusedRatherThanCut(t *testing.T) {
	fits := strings.Repeat("a", MaxRulesTextBytes)
	blocks, err := newPluginWithRules(t, projectRoot(t), fits).Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("a rule set at the text ceiling produced %d blocks, want 2", len(blocks))
	}

	tooLong := strings.Repeat("a", MaxRulesTextBytes+1)
	blocks, err = newPluginWithRules(t, projectRoot(t), tooLong).Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("an over-long rule set produced %d blocks, want the identity block alone", len(blocks))
	}
	if strings.Contains(blocks[0].Text, tooLong) {
		t.Fatalf("the refused rule set leaked into the identity block")
	}
}

// Every rules block stays inside the budget the descriptor asks the Kernel for:
// a block larger than its declared budget would be cut by the Kernel, and the
// capability never relies on that.
func TestTheRulesBlockFitsTheBudgetDeclaredForIt(t *testing.T) {
	declared := 0
	for _, c := range Descriptor().Contributions {
		if c.Kind == plugin.ContributionContext && c.ID == RulesContextID {
			declared = c.BudgetBytes
		}
	}
	if declared != RulesBudgetBytes {
		t.Fatalf("declared budget=%d, want %d", declared, RulesBudgetBytes)
	}
	blocks, err := newPluginWithRules(t, projectRoot(t), strings.Repeat("a", MaxRulesTextBytes)).Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks=%+v, want the identity and the rules blocks", blocks)
	}
	if size := len(blocks[1].Text); size == 0 || size > declared {
		t.Fatalf("rules block size=%d, want 1..%d bytes", size, declared)
	}
}
