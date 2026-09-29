package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentCommandsNeverFallThroughToServerOrImplicitAdoption(t *testing.T) {
	t.Setenv("LUNA_HOME", "invalid-relative-home")
	for _, args := range [][]string{{"dev"}, {"dev", "apply"}, {"dev", "create"}, {"dev", "status", "-workspace", "/missing"}, {"dev", "export", "-workspace", "/missing", "-dest", "/unused", "-include", ".env"}, {"dev", "inspect", "-bundle", "/missing"}} {
		handled, err := Handle(args, &bytes.Buffer{})
		if !handled || err == nil {
			t.Fatal("development operation admitted or reached server", args, handled, err)
		}
	}
}

func TestDevelopmentCLIProducesAnInspectableCandidate(t *testing.T) {
	t.Setenv("LUNA_HOME", "invalid-relative-home")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--template=")
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	commit := git("rev-parse", "HEAD")
	workspace := filepath.Join(t.TempDir(), "dev")
	bundle := filepath.Join(t.TempDir(), "candidate")
	call := func(args ...string) map[string]any {
		t.Helper()
		var out bytes.Buffer
		handled, err := Handle(append([]string{"dev"}, args...), &out)
		if !handled || err != nil {
			t.Fatal(handled, err)
		}
		var value map[string]any
		if err = json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	created := call("create", "-repo", repo, "-commit", commit, "-dest", workspace)
	if created["source_commit"] != commit {
		t.Fatal(created)
	}
	os.WriteFile(filepath.Join(workspace, "source/README.md"), []byte("after\n"), 0600)
	status := call("status", "-workspace", workspace)
	if len(status["changes"].([]any)) != 1 {
		t.Fatal(status)
	}
	call("export", "-workspace", workspace, "-dest", bundle)
	inspected := call("inspect", "-bundle", bundle)
	if inspected["source_commit"] != commit {
		t.Fatal(inspected)
	}
	raw, _ := os.ReadFile(filepath.Join(repo, "README.md"))
	if string(raw) != "before\n" {
		t.Fatal("CLI modified original repository")
	}
}
