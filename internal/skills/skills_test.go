package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates <root>/<dir>/SKILL.md with the given content.
func writeSkill(t *testing.T, root, dir, content string) string {
	t.Helper()
	path := filepath.Join(root, dir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, FileName)
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// skillFile builds a minimal SKILL.md.
func skillFile(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Body\n"
}

func TestDiscoversOneSkillPerDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", skillFile("alpha", "Do alpha things."))
	writeSkill(t, root, "beta", skillFile("beta", "Do beta things."))
	// A directory without SKILL.md is not a skill, and a plain file in the root
	// is not a skill directory either.
	if err := os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose.md"), []byte("---\nname: loose\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, problems := Discover([]Root{{Path: root, Scope: ScopeUser}})
	if len(problems) != 0 {
		t.Fatalf("problems=%v, want none", problems)
	}
	if len(found) != 2 {
		t.Fatalf("found %d skills, want 2: %+v", len(found), found)
	}
	if found[0].Name != "alpha" || found[1].Name != "beta" {
		t.Fatalf("order=%q,%q, want alpha,beta", found[0].Name, found[1].Name)
	}
	if found[0].Scope != ScopeUser || found[0].Dir != filepath.Join(root, "alpha") {
		t.Fatalf("skill=%+v", found[0])
	}
	if found[0].Description != "Do alpha things." {
		t.Fatalf("description=%q", found[0].Description)
	}
}

func TestSkipsANonDirectoryRootWithoutAProblem(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	file := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, problems := Discover([]Root{{Path: missing}, {Path: file}, {Path: ""}})
	if len(found) != 0 {
		t.Fatalf("found=%v, want none", found)
	}
	if len(problems) != 0 {
		t.Fatalf("problems=%v, want none: a root with nothing in it is not a fault", problems)
	}
}

func TestRejectsANameThatDoesNotMatchItsDirectory(t *testing.T) {
	root := t.TempDir()
	file := writeSkill(t, root, "alpha", skillFile("beta", "Do beta things."))

	found, problems := Discover([]Root{{Path: root}})
	if len(found) != 0 {
		t.Fatalf("found=%v, want none", found)
	}
	if len(problems) != 1 || problems[0].Path != file {
		t.Fatalf("problems=%v", problems)
	}
	if !strings.Contains(problems[0].Reason, "does not match the directory name") {
		t.Fatalf("reason=%q", problems[0].Reason)
	}
}

func TestRejectsAnIllegalOrOverlongName(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "Alpha", skillFile("Alpha", "Capitalised."))
	long := strings.Repeat("a", MaxNameBytes+1)
	writeSkill(t, root, long, skillFile(long, "Too long a name."))

	found, problems := Discover([]Root{{Path: root}})
	if len(found) != 0 {
		t.Fatalf("found=%v, want none", found)
	}
	if len(problems) != 2 {
		t.Fatalf("problems=%v, want one per rejected file", problems)
	}
	if !strings.Contains(problems[0].Reason, "not lowercase letters") {
		t.Fatalf("reason=%q", problems[0].Reason)
	}
	if !strings.Contains(problems[1].Reason, "over the 64-byte limit") {
		t.Fatalf("reason=%q", problems[1].Reason)
	}
}

func TestRejectsAMissingOrOverlongDescription(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "---\nname: alpha\n---\n\n# Body\n")
	writeSkill(t, root, "beta", "---\nname: beta\ndescription: \"\"\n---\n")
	writeSkill(t, root, "gamma", skillFile("gamma", strings.Repeat("字", MaxDescriptionChars+1)))
	writeSkill(t, root, "none", "# no frontmatter\n")

	found, problems := Discover([]Root{{Path: root}})
	if len(found) != 0 {
		t.Fatalf("found=%v, want none", found)
	}
	if len(problems) != 4 {
		t.Fatalf("problems=%v, want 4", problems)
	}
	// Reported in directory order: alpha, beta, gamma, none.
	for _, i := range []int{0, 1} {
		if !strings.Contains(problems[i].Reason, "no description") {
			t.Fatalf("problems[%d].reason=%q", i, problems[i].Reason)
		}
	}
	if !strings.Contains(problems[2].Reason, "over the 1024-character limit") {
		t.Fatalf("reason=%q", problems[2].Reason)
	}
	if !strings.Contains(problems[3].Reason, "no frontmatter block") {
		t.Fatalf("reason=%q", problems[3].Reason)
	}
}

func TestIgnoresUnknownFrontmatterFields(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", `---
name: alpha
description: Do alpha things.
license: MIT
compatibility: linux
version: "1.2.0"
category: software-development
tags: [one, two]
allowed-tools: Bash, Read
metadata:
  hermes:
    tags: [x]
template: something-this-version-does-not-know
future-field:
  nested: true
---

# Body
`)

	found, problems := Discover([]Root{{Path: root}})
	if len(problems) != 0 {
		t.Fatalf("problems=%v, want none: a skill is third-party content", problems)
	}
	if len(found) != 1 || found[0].Name != "alpha" || found[0].Description != "Do alpha things." {
		t.Fatalf("found=%+v", found)
	}
}

func TestReportsBrokenYAML(t *testing.T) {
	root := t.TempDir()
	file := writeSkill(t, root, "alpha", "---\nname: [unclosed\ndescription: x\n---\n")

	found, problems := Discover([]Root{{Path: root}})
	if len(found) != 0 {
		t.Fatalf("found=%v, want none", found)
	}
	if len(problems) != 1 || problems[0].Path != file {
		t.Fatalf("problems=%v", problems)
	}
	if !strings.Contains(problems[0].Reason, "not valid YAML") {
		t.Fatalf("reason=%q", problems[0].Reason)
	}
}

func TestReportsAnUnreadableSkillFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root user can read a file with no permission bits")
	}
	root := t.TempDir()
	file := writeSkill(t, root, "alpha", skillFile("alpha", "Do alpha things."))
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	found, problems := Discover([]Root{{Path: root}})
	if len(found) != 0 {
		t.Fatalf("found=%v, want none", found)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "cannot be read") {
		t.Fatalf("problems=%v, want one reported unreadable file", problems)
	}
}

func TestReportsASkillFileOverTheSizeLimit(t *testing.T) {
	root := t.TempDir()
	// Valid frontmatter, so the only thing that can reject this file is its
	// size: the fixture has to distinguish the branch being asserted.
	huge := "---\nname: alpha\ndescription: Do alpha things.\n---\n\n" + strings.Repeat("a", MaxSkillFileBytes)
	file := writeSkill(t, root, "alpha", huge)
	writeSkill(t, root, "beta", skillFile("beta", "Do beta things."))

	found, problems := Discover([]Root{{Path: root}})
	if len(found) != 1 || found[0].Name != "beta" {
		t.Fatalf("found=%+v, want only the skill inside the limit", found)
	}
	if len(problems) != 1 || problems[0].Path != file {
		t.Fatalf("problems=%v", problems)
	}
	if !strings.Contains(problems[0].Reason, "over the 262144-byte limit") {
		t.Fatalf("reason=%q, want the limit stated", problems[0].Reason)
	}
}

func TestKeepsTheHigherPrioritySkillAndReportsTheShadowedOne(t *testing.T) {
	builtin := t.TempDir()
	user := t.TempDir()
	project := t.TempDir()
	low := writeSkill(t, builtin, "alpha", skillFile("alpha", "Built-in alpha."))
	middle := writeSkill(t, user, "alpha", skillFile("alpha", "User alpha."))
	writeSkill(t, project, "alpha", skillFile("alpha", "Project alpha."))
	writeSkill(t, user, "only", skillFile("only", "Only here."))

	found, problems := Discover([]Root{
		{Path: builtin, Scope: ScopeBuiltin},
		{Path: user, Scope: ScopeUser},
		{Path: project, Scope: ScopeProject},
	})
	if len(found) != 2 {
		t.Fatalf("found=%+v, want the project alpha and the user-only skill", found)
	}
	if found[0].Name != "alpha" || found[0].Scope != ScopeProject || found[0].Description != "Project alpha." {
		t.Fatalf("kept=%+v, want the project-scope alpha", found[0])
	}
	if len(problems) != 2 {
		t.Fatalf("problems=%v, want both shadowed files reported", problems)
	}
	reported := problems[0].Path + " " + problems[1].Path
	if !strings.Contains(reported, low) || !strings.Contains(reported, middle) {
		t.Fatalf("problems=%v, want a report for each shadowed file", problems)
	}
	for _, problem := range problems {
		if !strings.Contains(problem.Reason, "shadowed by the") || !strings.Contains(problem.Reason, "of the same name") {
			t.Fatalf("reason=%q, want the shadowed file named along with what shadowed it", problem.Reason)
		}
	}
	if !strings.Contains(problems[0].Reason, "user skill") || !strings.Contains(problems[1].Reason, "project skill") {
		t.Fatalf("problems=%v, want each report to name the winner", problems)
	}
}

func TestLineCarriesTheNameTheScopeAndTheDescription(t *testing.T) {
	line := Skill{Name: "alpha", Description: "Do alpha things.", Scope: ScopeUser}.Line()
	if line != "- alpha (user) — Do alpha things." {
		t.Fatalf("line=%q", line)
	}
	if got := (Skill{Name: "alpha", Scope: ScopeBuiltin}).Line(); got != "- alpha (builtin)" {
		t.Fatalf("line=%q", got)
	}
}

func TestListRendersOneLinePerSkill(t *testing.T) {
	found := []Skill{
		{Name: "alpha", Description: "Do alpha things.", Scope: ScopeUser},
		{Name: "beta", Description: "Do beta things.", Scope: ScopeBuiltin},
	}
	text, cut := List(found, DefaultListBudget)
	if cut {
		t.Fatal("cut=true for a list well inside the budget")
	}
	want := "- alpha (user) — Do alpha things.\n- beta (builtin) — Do beta things."
	if text != want {
		t.Fatalf("text=%q, want %q", text, want)
	}
}

func TestListDropsDescriptionsBeforeNamesWhenOverBudget(t *testing.T) {
	found := []Skill{
		{Name: "alpha", Description: strings.Repeat("a", 200), Scope: ScopeUser},
		{Name: "beta", Description: strings.Repeat("b", 200), Scope: ScopeUser},
	}
	text, cut := List(found, 120)
	if !cut {
		t.Fatal("cut=false for a list over its budget")
	}
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "beta") {
		t.Fatalf("text=%q, want both names kept", text)
	}
	if strings.Contains(text, "aaaa") || strings.Contains(text, "bbbb") {
		t.Fatalf("text=%q, want the descriptions dropped before any name", text)
	}
	if !strings.Contains(text, "hit its 120-byte limit") || !strings.Contains(text, "2 of 2 skills") {
		t.Fatalf("text=%q, want the limit stated out loud", text)
	}
	if len(text) > 120 {
		t.Fatalf("text is %d bytes, over the budget it was given", len(text))
	}
}

func TestListCutsSkillsAndStillSaysItWasCut(t *testing.T) {
	found := []Skill{
		{Name: "alpha", Description: strings.Repeat("a", 100), Scope: ScopeUser},
		{Name: "beta", Description: strings.Repeat("b", 100), Scope: ScopeUser},
		{Name: "gamma", Description: strings.Repeat("c", 100), Scope: ScopeUser},
	}
	const budget = 128
	const noticeFormat = "(this list hit its %d-byte limit: %d of %d skills are listed, without their descriptions)"
	notice := func(shown int) string { return fmt.Sprintf(noticeFormat, budget, shown, 3) }
	// Fixture guard: this budget has to be one where two names plus the notice
	// fit and all three do not, or the test proves nothing.
	twoNames := "- alpha (user)\n- beta (user)"
	threeNames := twoNames + "\n- gamma (user)"
	if len(twoNames)+1+len(notice(2)) > budget || len(threeNames)+1+len(notice(3)) <= budget {
		t.Fatalf("fixture: budget %d does not separate two names from three", budget)
	}

	text, cut := List(found, budget)
	if !cut {
		t.Fatal("cut=false for a list that cannot fit")
	}
	if want := twoNames + "\n" + notice(2); text != want {
		t.Fatalf("text=%q, want %q", text, want)
	}
}

func TestListOfNothingIsEmpty(t *testing.T) {
	text, cut := List(nil, DefaultListBudget)
	if text != "" || cut {
		t.Fatalf("text=%q cut=%v, want an empty list with nothing to report", text, cut)
	}
}

func TestBodyDropsTheFrontmatter(t *testing.T) {
	raw := "---\nname: alpha\ndescription: x\n---\n\n# Heading\n\nSteps.\n"
	if got := Body(raw); got != "\n# Heading\n\nSteps.\n" {
		t.Fatalf("body=%q", got)
	}
	if got := Body(raw + "---\n"); !strings.Contains(got, "# Heading") {
		t.Fatalf("body=%q, want the body after the closing delimiter", got)
	}
	if got := Body("# no frontmatter\n"); got != "# no frontmatter\n" {
		t.Fatalf("body=%q, want the file unchanged", got)
	}
	if got := Body("---\nname: alpha\ndescription: x\n"); got != "---\nname: alpha\ndescription: x\n" {
		t.Fatalf("body=%q, want an unclosed block treated as no frontmatter", got)
	}
}
