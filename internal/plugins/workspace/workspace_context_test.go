package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// workspaceFor returns a lookup answering one session's workspace, the way the
// composition root builds it from a session's newest config record.
func workspaceFor(session string, target Target) Lookup {
	return func(id string) (Target, bool) {
		if id != session {
			return Target{}, false
		}
		return target, true
	}
}

// dirWithRules creates a directory holding an AGENTS.md with these contents.
func dirWithRules(t *testing.T, parent, name, rules string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, RulesFileName), []byte(rules), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	return dir
}

func blocksFor(t *testing.T, p *Plugin, session string) []plugin.ContextBlock {
	t.Helper()
	ctx := context.Background()
	if session != "" {
		ctx = plugin.WithRun(ctx, plugin.RunInfo{RunID: "run-1", SessionID: session})
	}
	blocks, err := p.Contexts(ctx)
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	return blocks
}

func blockOf(t *testing.T, blocks []plugin.ContextBlock, id string) plugin.ContextBlock {
	t.Helper()
	for _, block := range blocks {
		if block.ID == id {
			return block
		}
	}
	t.Fatalf("no %q block among %+v", id, blocks)
	return plugin.ContextBlock{}
}

// A session bound to a workspace is told the workspace's name and the names of
// its directories — never the absolute paths those names stand for, which are one
// machine's layout and not something the model needs.
func TestABoundSessionSeesTheWorkspaceByDirectoryNamesOnly(t *testing.T) {
	base := t.TempDir()
	alpha := dirWithRules(t, base, "proj-alpha", "# rules\n\nalpha rule\n")
	beta := dirWithRules(t, base, "proj-beta", "# rules\n\nbeta rule\n")
	p, err := New(Options{
		Root:   projectRoot(t),
		Lookup: workspaceFor("session-1", Target{Name: "alpha-beta", Dirs: []string{alpha, beta}}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	blocks := blocksFor(t, p, "session-1")
	identity := blockOf(t, blocks, ProjectContextID)
	if identity.Kind != plugin.ContextReference {
		t.Fatalf("identity block kind = %s, want reference", identity.Kind)
	}
	for _, want := range []string{workspaceBlockHeader, "Workspace: alpha-beta", "proj-alpha", "proj-beta"} {
		if !strings.Contains(identity.Text, want) {
			t.Fatalf("identity block does not contain %q:\n%s", want, identity.Text)
		}
	}
	rules := blockOf(t, blocks, RulesContextID)
	if rules.Kind != plugin.ContextInstruction {
		t.Fatalf("rules block kind = %s, want instruction", rules.Kind)
	}
	for _, want := range []string{rulesBlockHeader, `Rules from "proj-alpha"`, "alpha rule", `Rules from "proj-beta"`, "beta rule"} {
		if !strings.Contains(rules.Text, want) {
			t.Fatalf("rules block does not contain %q:\n%s", want, rules.Text)
		}
	}
	for _, block := range blocks {
		if strings.Contains(block.Text, base) {
			t.Fatalf("the %s block carries the host path %q:\n%s", block.ID, base, block.Text)
		}
	}
	if len(rules.Text) > RulesBudgetBytes || len(identity.Text) > ProjectBudgetBytes {
		t.Fatalf("a block is over its declared budget: identity=%d rules=%d", len(identity.Text), len(rules.Text))
	}
}

// A directory with no AGENTS.md states no rules, and the directories that do are
// still read: one silent directory does not take the others' rules with it.
func TestADirectoryWithoutRulesIsSimplySilent(t *testing.T) {
	base := t.TempDir()
	alpha := dirWithRules(t, base, "proj-alpha", "alpha rule\n")
	beta := filepath.Join(base, "proj-beta")
	if err := os.MkdirAll(beta, 0o700); err != nil {
		t.Fatalf("create %s: %v", beta, err)
	}
	p, err := New(Options{Root: projectRoot(t), Lookup: workspaceFor("s", Target{Name: "two", Dirs: []string{alpha, beta}})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rules := blockOf(t, blocksFor(t, p, "s"), RulesContextID)
	if !strings.Contains(rules.Text, "alpha rule") {
		t.Fatalf("rules from the directory that has them are missing:\n%s", rules.Text)
	}
	if strings.Contains(rules.Text, "proj-beta") {
		t.Fatalf("a directory with no rules was mentioned anyway:\n%s", rules.Text)
	}
}

// Rules too large for this run are dropped and reported, never cut: a rule that
// stops mid-sentence is a rule the directory never wrote.
func TestOversizedRulesAreReportedRatherThanTruncated(t *testing.T) {
	base := t.TempDir()
	huge := strings.Repeat("x", MaxRulesTextBytes*2)
	alpha := dirWithRules(t, base, "proj-alpha", huge)
	var problems []string
	p, err := New(Options{
		Root:   projectRoot(t),
		Lookup: workspaceFor("s", Target{Name: "one", Dirs: []string{alpha}}),
		Report: func(problem string) { problems = append(problems, problem) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	blocks := blocksFor(t, p, "s")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v, want only the identity block", blocks)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "proj-alpha") {
		t.Fatalf("problems = %v, want one statement naming the directory", problems)
	}
	if !strings.Contains(problems[0], RulesFileName) {
		t.Fatalf("problem %q does not name the file", problems[0])
	}
}

// A session that is not bound to a workspace is told exactly what it was told
// before workspaces existed: the fallback project and its rules. This is what
// keeps every existing session's context unchanged.
func TestASessionWithoutAWorkspaceFallsBack(t *testing.T) {
	base := t.TempDir()
	alpha := dirWithRules(t, base, "proj-alpha", "alpha rule\n")
	p, err := New(Options{
		Root:   projectRoot(t),
		Rules:  "fallback rule",
		Lookup: workspaceFor("bound", Target{Name: "one", Dirs: []string{alpha}}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, session := range []string{"unbound", ""} {
		blocks := blocksFor(t, p, session)
		identity := blockOf(t, blocks, ProjectContextID)
		if identity.Text != p.Render() {
			t.Fatalf("session %q identity = %q, want the fallback block", session, identity.Text)
		}
		rules := blockOf(t, blocks, RulesContextID)
		if !strings.Contains(rules.Text, "fallback rule") || strings.Contains(rules.Text, "alpha rule") {
			t.Fatalf("session %q rules = %q, want the fallback rules", session, rules.Text)
		}
	}
}

// A workspace of many directories cannot fill the prompt: every directory's rules
// get a share of the declared budget, and the total stays inside it.
func TestManyDirectoriesShareTheRulesBudget(t *testing.T) {
	base := t.TempDir()
	dirs := make([]string, 0, 8)
	for _, name := range []string{"a-directory", "b-directory", "c-directory", "d-directory"} {
		dirs = append(dirs, dirWithRules(t, base, name, strings.Repeat("rule text ", 40)))
	}
	p, err := New(Options{Root: projectRoot(t), Lookup: workspaceFor("s", Target{Name: "many", Dirs: dirs})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	blocks := blocksFor(t, p, "s")
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v, want identity and rules", blocks)
	}
	if got := len(blocks[1].Text); got > RulesBudgetBytes {
		t.Fatalf("rules block is %d bytes, over the %d-byte budget", got, RulesBudgetBytes)
	}
}
