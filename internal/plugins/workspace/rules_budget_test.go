package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRulesUseActualSpaceInsteadOfReservingItForEmptyDirectories(t *testing.T) {
	base := t.TempDir()
	large := strings.Repeat("rule ", 4000) + "LARGE_END"
	small := strings.Repeat("note ", 800) + "SMALL_END"
	dirs := []string{dirWithRules(t, base, "main-project", large), dirWithRules(t, base, "side-project", small)}
	for _, name := range []string{"assets", "samples", "output"} {
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	var problems []string
	p, err := New(Options{Root: base, Lookup: workspaceFor("s", Target{Name: "many", Dirs: dirs}), Report: func(problem string) { problems = append(problems, problem) }})
	if err != nil {
		t.Fatal(err)
	}
	blocks := blocksFor(t, p, "s")
	var text string
	for _, block := range blocks {
		if block.ID == RulesContextID {
			text = block.Text
		}
	}
	if !strings.Contains(text, large) || !strings.Contains(text, small) {
		t.Fatalf("complete rules missing: returned %d bytes, problems=%v", len(text), problems)
	}
	if len(problems) != 0 {
		t.Fatalf("fitting rules reported as unavailable: %v", problems)
	}
}

func TestRulesBudgetSkipsWholeFilesInOrderAndCanKeepLaterSmallFiles(t *testing.T) {
	base := t.TempDir()
	first := strings.Repeat("A", RulesBudgetBytes-256) + "FIRST_END"
	second := "SECOND_BEGIN" + strings.Repeat("B", 600) + "SECOND_END"
	tiny := "TINY_END"
	dirs := []string{dirWithRules(t, base, "a", first), dirWithRules(t, base, "b", second), dirWithRules(t, base, "c", tiny)}
	var problems []string
	p, err := New(Options{Root: base, Lookup: workspaceFor("s", Target{Name: "ordered", Dirs: dirs}), Report: func(problem string) { problems = append(problems, problem) }})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, block := range blocksFor(t, p, "s") {
		if block.ID == RulesContextID {
			text = block.Text
		}
	}
	if !strings.Contains(text, first) || !strings.Contains(text, tiny) || strings.Contains(text, "SECOND_BEGIN") || len(text) > RulesBudgetBytes {
		t.Fatalf("incorrect complete-file selection: %d bytes, problems=%v", len(text), problems)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "\"b\"") {
		t.Fatalf("missing specific budget warning: %v", problems)
	}
}
