package devworkspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitFixture(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git fixture: %v %s", err, raw)
	}
	return strings.TrimSpace(string(raw))
}
func fixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "internal/example"), 0700)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("baseline\n"), 0600)
	os.WriteFile(filepath.Join(repo, "internal/example/main.go"), []byte("package example\n"), 0600)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.24\n"), 0600)
	gitFixture(t, repo, "init", "-q", "--template=")
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	return repo, gitFixture(t, repo, "rev-parse", "HEAD")
}
func TestCreateUsesPinnedSourceWithoutPrivateOrSharedGitState(t *testing.T) {
	repo, commit := fixture(t)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("uncommitted\n"), 0600)
	os.WriteFile(filepath.Join(repo, ".env"), []byte("PRIVATE_FIXTURE"), 0600)
	dest := filepath.Join(t.TempDir(), "candidate")
	m, err := Create(context.Background(), repo, commit, dest)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceCommit != commit {
		t.Fatal("source identity lost")
	}
	raw, err := os.ReadFile(filepath.Join(dest, "source/README.md"))
	if err != nil || string(raw) != "baseline\n" {
		t.Fatal("copied uncommitted content", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "source/.env")); !os.IsNotExist(err) {
		t.Fatal("copied private file")
	}
	original, _ := os.Stat(filepath.Join(repo, "internal/example/main.go"))
	copied, _ := os.Stat(filepath.Join(dest, "source/internal/example/main.go"))
	if os.SameFile(original, copied) {
		t.Fatal("shared source inode")
	}
	gitFixture(t, filepath.Join(dest, "source"), "status", "--porcelain")
	if _, err := Create(context.Background(), repo, commit, dest); err == nil {
		t.Fatal("overwrote candidate")
	}
	if _, err := Create(context.Background(), repo, commit, filepath.Join(repo, "nested")); err == nil {
		t.Fatal("candidate created inside original repository")
	}
}
func TestExportIncludesExplicitNewFilesDeletionAndExecutableMode(t *testing.T) {
	repo, commit := fixture(t)
	workspace := filepath.Join(t.TempDir(), "dev")
	if _, err := Create(context.Background(), repo, commit, workspace); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "source")
	os.WriteFile(filepath.Join(source, "README.md"), []byte("improved\n"), 0600)
	os.Remove(filepath.Join(source, "internal/example/main.go"))
	os.WriteFile(filepath.Join(source, "helper.sh"), []byte("#!/bin/sh\nprintf helper\n"), 0700)
	status, err := Status(context.Background(), workspace)
	if err != nil || len(status.Untracked) != 1 || len(status.Changes) != 2 {
		t.Fatal(status, err)
	}
	bundle := filepath.Join(t.TempDir(), "proposal")
	if _, err := Export(context.Background(), workspace, bundle, nil); err == nil {
		t.Fatal("silently omitted new source")
	}
	report, err := Export(context.Background(), workspace, bundle, []string{"helper.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 3 {
		t.Fatal(report.Changes)
	}
	if _, err := Inspect(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	applied := t.TempDir()
	if err := copyBaselineForTest(workspace, applied); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, applied, "init", "-q", "--template=")
	gitFixture(t, applied, "apply", "--check", filepath.Join(bundle, PatchName))
	gitFixture(t, applied, "apply", filepath.Join(bundle, PatchName))
	raw, _ := os.ReadFile(filepath.Join(applied, "README.md"))
	if string(raw) != "improved\n" {
		t.Fatal("patch did not apply actual changes")
	}
	if _, err := os.Stat(filepath.Join(applied, "internal/example/main.go")); !os.IsNotExist(err) {
		t.Fatal("deletion not represented")
	}
	info, err := os.Stat(filepath.Join(applied, "helper.sh"))
	if err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("new executable mode lost")
	}
	raw, _ = os.ReadFile(filepath.Join(repo, "README.md"))
	if string(raw) != "baseline\n" {
		t.Fatal("export changed original repository")
	}
}
func copyBaselineForTest(workspace, dest string) error {
	return filepath.WalkDir(filepath.Join(workspace, "baseline"), func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(filepath.Join(workspace, "baseline"), name)
		target := filepath.Join(dest, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
}
func TestExportIgnoresCandidateGitCodeAndRejectsTampering(t *testing.T) {
	repo, commit := fixture(t)
	workspace := filepath.Join(t.TempDir(), "dev")
	if _, err := Create(context.Background(), repo, commit, workspace); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "source")
	marker := filepath.Join(t.TempDir(), "executed")
	hook := "#!/bin/sh\ntouch '" + marker + "'\n"
	os.MkdirAll(filepath.Join(source, ".git/hooks"), 0700)
	os.WriteFile(filepath.Join(source, ".git/hooks/pre-commit"), []byte(hook), 0700)
	os.WriteFile(filepath.Join(source, ".git/config"), []byte("[core]\n fsmonitor = touch "+marker+"\n[filter \"evil\"]\n clean = touch "+marker+"\n[diff \"evil\"]\n textconv = touch "+marker+"\n"), 0600)
	os.WriteFile(filepath.Join(source, ".gitattributes"), []byte("* filter=evil diff=evil\n"), 0600)
	os.WriteFile(filepath.Join(source, "README.md"), []byte("improved\n"), 0600)
	bundle := filepath.Join(t.TempDir(), "proposal")
	if _, err := Export(context.Background(), workspace, bundle, []string{".gitattributes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("candidate Git code executed")
	}
	os.WriteFile(filepath.Join(bundle, PatchName), []byte("changed patch"), 0600)
	if _, err := Inspect(context.Background(), bundle); err == nil {
		t.Fatal("tampered patch accepted")
	}
	os.WriteFile(filepath.Join(workspace, "baseline/README.md"), []byte("changed baseline"), 0600)
	if _, err := Status(context.Background(), workspace); err == nil {
		t.Fatal("tampered baseline accepted")
	}
}
func TestStatusRejectsLinksAndExportCannotIncludePrivateFiles(t *testing.T) {
	repo, commit := fixture(t)
	workspace := filepath.Join(t.TempDir(), "dev")
	if _, err := Create(context.Background(), repo, commit, workspace); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "source")
	os.WriteFile(filepath.Join(source, ".env"), []byte("PRIVATE_FIXTURE"), 0600)
	if _, err := Export(context.Background(), workspace, filepath.Join(t.TempDir(), "bundle"), []string{".env"}); err == nil {
		t.Fatal("private file explicitly exported")
	}
	os.Symlink("/etc/passwd", filepath.Join(source, "link"))
	if _, err := Status(context.Background(), workspace); err == nil {
		t.Fatal("candidate symlink accepted")
	}
}

func TestBinaryPatchAndModeOnlyChangeRespectExplicitIgnoredFiles(t *testing.T) {
	repo, commit := fixture(t)
	workspace := filepath.Join(t.TempDir(), "dev")
	if _, err := Create(context.Background(), repo, commit, workspace); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "source")
	os.Chmod(filepath.Join(source, "internal/example/main.go"), 0700)
	binary := []byte{0, 1, 2, 3, 13, 10, 0, 255}
	os.WriteFile(filepath.Join(source, "asset.bin"), binary, 0600)
	os.WriteFile(filepath.Join(source, ".gitignore"), []byte("asset.bin\n"), 0600)
	bundle := filepath.Join(t.TempDir(), "proposal")
	if _, err := Export(context.Background(), workspace, bundle, []string{"asset.bin", ".gitignore"}); err != nil {
		t.Fatal(err)
	}
	applied := t.TempDir()
	if err := copyBaselineForTest(workspace, applied); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, applied, "init", "-q", "--template=")
	gitFixture(t, applied, "apply", filepath.Join(bundle, PatchName))
	raw, err := os.ReadFile(filepath.Join(applied, "asset.bin"))
	if err != nil || !bytes.Equal(raw, binary) {
		t.Fatal("binary patch or explicitly ignored file lost", err)
	}
	info, err := os.Stat(filepath.Join(applied, "internal/example/main.go"))
	if err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("mode-only modification lost")
	}
}
