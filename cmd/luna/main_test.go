package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootFromExecutableRuntimeBinary(t *testing.T) {
	got := rootFromExecutable("/tmp/luna-agent/.runtime/luna")
	if got != "/tmp/luna-agent" {
		t.Fatalf("got %q", got)
	}
}

// makeRoot creates a directory shaped like a repository root: it holds the
// plugins directory and a web/index.html asset.
func makeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatalf("create plugins dir: %v", err)
	}
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatalf("create web dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write web/index.html: %v", err)
	}
	return root
}

func TestHasAssetsRequiresBothAssets(t *testing.T) {
	webOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(webOnly, "web"), 0o755); err != nil {
		t.Fatalf("create web dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webOnly, "web", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	pluginsOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginsOnly, "plugins"), 0o755); err != nil {
		t.Fatalf("create plugins dir: %v", err)
	}

	pluginsIsAFile := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginsIsAFile, "web"), 0o755); err != nil {
		t.Fatalf("create web dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsIsAFile, "web", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsIsAFile, "plugins"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write plugins file: %v", err)
	}

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"complete root", makeRoot(t), true},
		{"missing plugins", webOnly, false},
		{"missing web/index.html", pluginsOnly, false},
		{"plugins is a file", pluginsIsAFile, false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		if got := hasAssets(tc.dir); got != tc.want {
			t.Fatalf("%s: hasAssets(%q) = %v, want %v", tc.name, tc.dir, got, tc.want)
		}
	}
}

func TestResolveRootUsesExplicitRoot(t *testing.T) {
	explicit := makeRoot(t)
	cwd := makeRoot(t)
	got, err := resolveRoot(explicit, filepath.Join(cwd, ".runtime", "luna"), cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != explicit {
		t.Fatalf("got %q, want the explicit root %q", got, explicit)
	}
}

// The working directory is the weakest candidate, so its absence must never
// block an explicit root. This is the case that a removed working directory
// (a deleted checkout, an unlinked temporary directory) produces in practice.
func TestResolveRootKeepsExplicitRootWithoutWorkingDirectory(t *testing.T) {
	explicit := makeRoot(t)
	got, err := resolveRoot(explicit, "/nonexistent/.runtime/luna", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != explicit {
		t.Fatalf("got %q, want the explicit root %q", got, explicit)
	}
}

func TestResolveRootRejectsInvalidExplicitRoot(t *testing.T) {
	invalid := t.TempDir() // exists, but holds neither web/ nor plugins/
	exeRoot := makeRoot(t)
	if _, err := resolveRoot(invalid, filepath.Join(exeRoot, ".runtime", "luna"), exeRoot); err == nil {
		t.Fatal("expected an error for an explicit root without web/ and plugins/")
	}
}

func TestResolveRootPrefersExecutableSibling(t *testing.T) {
	exeRoot := makeRoot(t)
	cwdRoot := makeRoot(t)
	got, err := resolveRoot("", filepath.Join(exeRoot, ".runtime", "luna"), cwdRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != exeRoot {
		t.Fatalf("got %q, want the executable sibling root %q", got, exeRoot)
	}
}

// A `go run ./cmd/luna` invocation places the executable in a temporary build
// directory, so the only usable candidate is the working directory.
func TestResolveRootFallsBackToWorkingDirectory(t *testing.T) {
	cwdRoot := makeRoot(t)
	goRunExe := filepath.Join(t.TempDir(), "go-build123", "b001", "exe", "luna")
	got, err := resolveRoot("", goRunExe, cwdRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != cwdRoot {
		t.Fatalf("got %q, want the working directory %q", got, cwdRoot)
	}
}

func TestResolveRootSkipsEmptyWorkingDirectory(t *testing.T) {
	exeRoot := makeRoot(t)
	got, err := resolveRoot("", filepath.Join(exeRoot, ".runtime", "luna"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != exeRoot {
		t.Fatalf("got %q, want the executable sibling root %q", got, exeRoot)
	}
}

// A relative -root is usable because the process never changes directory.
func TestResolveRootAcceptsRelativeExplicitRoot(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot read the working directory: %v", err)
	}
	if err := os.Chdir(makeRoot(t)); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	got, err := resolveRoot(".", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "." {
		t.Fatalf("got %q, want %q", got, ".")
	}
}

func TestResolveRootErrorNamesTriedPaths(t *testing.T) {
	exeRoot := t.TempDir() // exists, holds neither asset
	cwd := t.TempDir()     // a distinct directory, also without assets
	exe := filepath.Join(exeRoot, ".runtime", "luna")
	_, err := resolveRoot("", exe, cwd)
	if err == nil {
		t.Fatal("expected an error when no candidate holds web/ and plugins/")
	}
	for _, want := range []string{exeRoot, cwd} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name the tried path %q", err, want)
		}
	}
}

// The executable grandparent and the working directory are the same string in
// the common `./.runtime/luna` layout, so the error must not name it twice.
func TestResolveRootDeduplicatesCandidates(t *testing.T) {
	shared := t.TempDir()
	exe := filepath.Join(shared, ".runtime", "luna")
	_, err := resolveRoot("", exe, shared)
	if err == nil {
		t.Fatal("expected an error when the shared candidate holds no assets")
	}
	if count := strings.Count(err.Error(), shared); count != 1 {
		t.Fatalf("error %q names the shared candidate %d times, want 1", err, count)
	}
}

func TestSessionsDirDefaultsUnderRoot(t *testing.T) {
	got := sessionsDir("/repo", "")
	want := filepath.Join("/repo", ".runtime", "sessions")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSessionsDirPrefersExplicitValue(t *testing.T) {
	got := sessionsDir("/repo", "/elsewhere/sessions")
	if got != "/elsewhere/sessions" {
		t.Fatalf("got %q, want the explicit value", got)
	}
}

func TestMemoryFileDefaultsUnderRoot(t *testing.T) {
	got := memoryFile("/repo", "")
	want := filepath.Join("/repo", ".runtime", "memory.jsonl")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMemoryFilePrefersExplicitValue(t *testing.T) {
	got := memoryFile("/repo", "/elsewhere/memory.jsonl")
	if got != "/elsewhere/memory.jsonl" {
		t.Fatalf("got %q, want the explicit value", got)
	}
}

// The default memory file sits next to the session directory, both under the
// git-ignored .runtime/, so neither is a repository artifact.
func TestMemoryAndSessionsShareTheRuntimeDirectory(t *testing.T) {
	root := "/repo"
	if filepath.Dir(memoryFile(root, "")) != filepath.Dir(sessionsDir(root, "")) {
		t.Fatalf("memory %q and sessions %q do not share a directory", memoryFile(root, ""), sessionsDir(root, ""))
	}
}

// UI plugins are discovered under the fixed plugins/ui convention, derived from
// the resolved root and never from a request.
func TestUIPluginsDirIsUnderTheResolvedRoot(t *testing.T) {
	got := uiPluginsDir("/repo")
	if want := filepath.Join("/repo", "plugins", "ui"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWorkingDirOrEmptyWhenDirectoryIsRemoved(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot read the working directory: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.Remove(dir); err != nil {
		t.Fatalf("remove the working directory: %v", err)
	}
	if got := workingDirOrEmpty(); got != "" {
		t.Fatalf("got %q, want an empty string when the working directory is gone", got)
	}
}
