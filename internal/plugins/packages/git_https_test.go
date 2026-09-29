package packages

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPSGitSourceUsesPinnedCommitWithoutUserCredentials(t *testing.T) {
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repository := packageSource(t)
	os.Remove(filepath.Join(repository, ".env"))
	os.Mkdir(filepath.Join(repository, "tools"), 0700)
	os.Rename(filepath.Join(repository, "tool.py"), filepath.Join(repository, "tools/tool.py"))
	manifest := sampleManifest()
	manifest.Files = []string{"tools/tool.py"}
	manifest.Tools[0].Entry = "tools/tool.py"
	raw, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(repository, ManifestName), raw, 0600)
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"-C", repository}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--template=")
	git("add", ManifestName, "tools/tool.py")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	commit := git("rev-parse", "HEAD")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("private credential configuration reached source server")
			http.Error(w, "unexpected authorization", 403)
			return
		}
		args := []string{"upload-pack", "--stateless-rpc"}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/package/info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			w.Write([]byte("001e# service=git-upload-pack\n0000"))
			args = append(args, "--advertise-refs")
		case r.Method == http.MethodPost && r.URL.Path == "/package/git-upload-pack":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		default:
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append(args, repository)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		cmd.Stdin = http.MaxBytesReader(w, r.Body, 1<<20)
		cmd.Stdout = w
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil {
			t.Errorf("Git HTTPS fixture backend failed: %v", err)
		}
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	// 夹具包装器仅信任这次合成CA；被测实现仍走真实HTTPS和git-upload-pack。
	wrapper := t.TempDir()
	script := "#!/bin/sh\nexec '" + binary + "' -c http.sslCAInfo='" + ca + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: SYNTHETIC_NOT_TO_INHERIT")
	store, _ := OpenStore(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	version, err := store.Stage(ctx, Source{Kind: "git", Location: server.URL + "/package", Revision: commit})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 || version.Source.Revision != commit {
		t.Fatal("real HTTPS flow did not run", requests.Load())
	}
	if _, err = os.Stat(filepath.Join(version.Root, "tools/tool.py")); err != nil {
		t.Fatal("nested source file was not installed", err)
	}
}
