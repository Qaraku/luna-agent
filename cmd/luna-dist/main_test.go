package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sourceFixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for _, name := range sourcePaths {
		if name == "go.mod" || name == "go.sum" || name == "LICENSE" {
			os.WriteFile(filepath.Join(repo, name), []byte("fixture"), 0600)
		} else {
			os.MkdirAll(filepath.Join(repo, name), 0700)
			os.WriteFile(filepath.Join(repo, name, "fixture.txt"), []byte("committed"), 0600)
		}
	}
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	return repo, git("rev-parse", "HEAD")
}
func TestExportUsesCommitObjectsNotCheckoutOrLocalAttributes(t *testing.T) {
	repo, commit := sourceFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "internal/fixture.txt"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git/info/attributes"), []byte("internal/fixture.txt export-ignore\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal/private.env"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := exportSource(ctx, repo, commit, dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "internal/fixture.txt"))
	if err != nil || string(data) != "committed" {
		t.Fatalf("export drifted from commit: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "internal/private.env")); !os.IsNotExist(err) {
		t.Fatal("copied untracked private file")
	}
}
func TestExportRejectsTrackedSymlinks(t *testing.T) {
	repo, _ := sourceFixture(t)
	os.Symlink("/etc/passwd", filepath.Join(repo, "internal/link"))
	cmd := exec.Command("git", "-C", repo, "add", "internal/link")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "link")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := exportSource(ctx, repo, strings.TrimSpace(string(raw)), t.TempDir()); err == nil {
		t.Fatal("copied tracked symlink")
	}
}
