package packages

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func packageSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	m := sampleManifest()
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "tool.py"), []byte("print('fixture')\n"), 0600)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("PRIVATE_FIXTURE"), 0000)
	return dir
}
func TestLocalPackageStageIsImmutableAndCopiesOnlyDeclaredFiles(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := packageSource(t)
	first, err := store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(first.Root, ".env")); !os.IsNotExist(err) {
		t.Fatal("undeclared private file copied")
	}
	old, _ := os.ReadFile(filepath.Join(first.Root, "tool.py"))
	os.WriteFile(filepath.Join(source, "tool.py"), []byte("print('new')\n"), 0600)
	second, err := store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision {
		t.Fatal("changed content reused revision")
	}
	kept, _ := os.ReadFile(filepath.Join(first.Root, "tool.py"))
	if string(kept) != string(old) {
		t.Fatal("staging changed old revision")
	}
	if _, err = store.Version("sample", first.Revision); err != nil {
		t.Fatal(err)
	}
}
func TestStageRejectsLinksAndTamperedInstalledVersion(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	source := packageSource(t)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Remove(filepath.Join(source, "tool.py"))
	os.Symlink(outside, filepath.Join(source, "tool.py"))
	if _, err := store.Stage(context.Background(), Source{Kind: "local", Location: source}); err == nil {
		t.Fatal("staged symbolic link")
	}
	source = packageSource(t)
	version, err := store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(version.Root, "tool.py"), []byte("tampered"), 0600)
	if _, err = store.Version("sample", version.Revision); err == nil {
		t.Fatal("tampered version accepted")
	}
}
func TestGitPackageStageUsesExactCommitWithoutCheckoutHooks(t *testing.T) {
	source := packageSource(t)
	os.Remove(filepath.Join(source, ".env"))
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("add", ManifestName, "tool.py")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	sha := git("rev-parse", "HEAD")
	sha = sha[:len(sha)-1]
	store, _ := OpenStore(t.TempDir())
	version, err := store.Stage(context.Background(), Source{Kind: "git", Location: source, Revision: sha})
	if err != nil {
		t.Fatal(err)
	}
	if version.Source.Revision != sha {
		t.Fatal("source revision not preserved")
	}
	if _, err = store.Stage(context.Background(), Source{Kind: "git", Location: source, Revision: "main"}); err == nil {
		t.Fatal("mutable branch ref accepted")
	}
}

func TestStageRejectsNormalizedManifestBeyondReadLimit(t *testing.T) {
	source := t.TempDir()
	m := sampleManifest()
	base := m.Tools[0]
	m.Tools = nil
	for i := 0; i < 16; i++ {
		tool := base
		tool.Name = fmt.Sprintf("echo-%d", i)
		tool.Input = Schema{Type: "object", Properties: map[string]*Schema{}}
		for j := 0; j < 32; j++ {
			tool.Input.Properties[fmt.Sprintf("field_%d", j)] = &Schema{Type: "string"}
		}
		m.Tools = append(m.Tools, tool)
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > MaxManifestBytes {
		t.Fatal("fixture exceeds source limit", err)
	}
	if err := os.WriteFile(filepath.Join(source, ManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "tool.py"), []byte("print('fixture')"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err == nil || !strings.Contains(err.Error(), "normalized manifest") {
		t.Fatalf("want normalized manifest limit, got %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(store.dir, "versions", "sample")); len(entries) != 0 {
		t.Fatal("unreadable candidate was published")
	}
}

func TestGitPackageSupportsSHA256RepositoryAndRejectsManifestLink(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			source := packageSource(t)
			os.Remove(filepath.Join(source, ".env"))
			git := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = source
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				raw, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git fixture: %v %s", err, raw)
				}
				return strings.TrimSpace(string(raw))
			}
			git("init", "-q", "--object-format="+format)
			git("add", ManifestName, "tool.py")
			git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
			commit := git("rev-parse", "HEAD")
			store, _ := OpenStore(t.TempDir())
			if _, err := store.Stage(context.Background(), Source{Kind: "git", Location: source, Revision: commit}); err != nil {
				t.Fatal("supported Git object format refused", err)
			}
			manifest, err := os.ReadFile(filepath.Join(source, ManifestName))
			if err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(source, ManifestName))
			if err = os.Symlink(string(manifest), filepath.Join(source, ManifestName)); err != nil {
				t.Fatal(err)
			}
			git("add", ManifestName)
			git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "manifest link")
			commit = git("rev-parse", "HEAD")
			if _, err := store.Stage(context.Background(), Source{Kind: "git", Location: source, Revision: commit}); err == nil {
				t.Fatal("Git manifest symlink was accepted as a regular manifest")
			}
		})
	}
}

func TestPackageStoreCountsAllStagedIdentitiesTowardItsLimit(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := Source{Kind: "local", Location: t.TempDir()}
	for i := 0; i < MaxInstalledPackages; i++ {
		m := sampleManifest()
		m.ID = fmt.Sprintf("package-%d", i)
		if _, err := store.stageFiles(context.Background(), source, m, map[string]packageFile{"tool.py": {data: []byte("fixture")}}); err != nil {
			t.Fatal(i, err)
		}
	}
	m := sampleManifest()
	m.ID = "one-more"
	if _, err := store.stageFiles(context.Background(), source, m, map[string]packageFile{"tool.py": {data: []byte("fixture")}}); err == nil {
		t.Fatal("staged identities grew beyond the disk quota before catalog registration")
	}
	entries, err := os.ReadDir(filepath.Join(store.dir, "versions"))
	if err != nil || len(entries) != MaxInstalledPackages {
		t.Fatal("identity quota was not enforced", len(entries), err)
	}
}
func TestConcurrentPackageStagesCannotOverfillRevisionQuota(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := Source{Kind: "local", Location: t.TempDir()}
	start := make(chan struct{})
	done := make(chan error, MaxPackageRevisions+8)
	for i := 0; i < MaxPackageRevisions+8; i++ {
		go func(i int) {
			<-start
			m := sampleManifest()
			_, err := store.stageFiles(context.Background(), source, m, map[string]packageFile{"tool.py": {data: []byte(fmt.Sprint(i))}})
			done <- err
		}(i)
	}
	close(start)
	success := 0
	for i := 0; i < MaxPackageRevisions+8; i++ {
		if <-done == nil {
			success++
		}
	}
	entries, err := os.ReadDir(filepath.Join(store.dir, "versions", "sample"))
	if err != nil || len(entries) != MaxPackageRevisions || success != MaxPackageRevisions {
		t.Fatalf("quota drift: successes=%d revisions=%d err=%v", success, len(entries), err)
	}
}
