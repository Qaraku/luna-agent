package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugins/workspace"
)

func TestTypicalChineseProjectRulesLoadCompletely(t *testing.T) {
	text := strings.Repeat("项目规则应完整交付，不要截断。\n", 260) + "最终规则标记"
	if len(text) <= 4096 || len(text) >= 24*1024 {
		t.Fatal("bad representative fixture")
	}
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got, problem := loadRules(path, workspace.MaxRulesTextBytes)
	if problem != "" || got != text {
		t.Fatalf("rule file %d bytes not loaded completely; got %d bytes, problem=%s", len(text), len(got), problem)
	}
}

func TestDefaultProjectRulesUseTheReadRootBoundary(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-rules")
	if err := os.WriteFile(outside, []byte("synthetic outside rule"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	text, problem := loadProjectRules(root, "", workspace.MaxRulesTextBytes)
	if text != "" || problem == "" {
		t.Fatalf("escaping rules were read: %d bytes, problem=%q", len(text), problem)
	}
	if strings.Contains(problem, outside) {
		t.Fatal("problem exposed the outside absolute path")
	}
	// 显式 CLI 文件是用户自己的选择，不与自动发现的规则路径混同。
	text, problem = loadProjectRules(root, outside, workspace.MaxRulesTextBytes)
	if problem != "" || text != "synthetic outside rule" {
		t.Fatalf("explicit override stopped working: %q", problem)
	}
}

func TestRuleFilesRejectNULInsteadOfInjectingBinaryText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("rule\x00binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if text, problem := loadRules(path, workspace.MaxRulesTextBytes); text != "" || problem == "" {
		t.Fatalf("binary rules not refused: %d bytes, problem=%q", len(text), problem)
	}
}

func TestCurrentRepositoryRulesFitTheNormalStartupPath(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	text, problem := loadProjectRules(root, "", workspace.MaxRulesTextBytes)
	if problem != "" || text != strings.TrimSpace(string(raw)) {
		t.Fatalf("current rules were not fully loaded: file=%d result=%d problem=%q", len(raw), len(text), problem)
	}
}

func TestOversizedRulesWithWhitespaceAreNeverAcceptedAsAPrefix(t *testing.T) {
	limit := workspace.MaxRulesTextBytes
	path := filepath.Join(t.TempDir(), "chosen.md")
	body := strings.Repeat(" ", 100) + strings.Repeat("r", limit) + "MUST_NOT_DROP_END"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	text, problem := loadProjectRules(t.TempDir(), path, limit)
	if text != "" || problem == "" {
		t.Fatalf("oversized rules accepted a %d-byte prefix without a problem", len(text))
	}
}
