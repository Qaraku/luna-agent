package fileread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testRoot builds a small tree:
//
//	<root>/docs/readme.md      "hello\n"
//	<root>/docs/../docs/inside.md
//	<root>/link-inside  -> docs/readme.md
//	<root>/link-outside -> <outside>/secret.txt
func testRoot(t *testing.T) (root, outside string) {
	t.Helper()
	root = t.TempDir()
	outside = t.TempDir()
	mustWrite(t, filepath.Join(root, "docs", "readme.md"), "hello\n")
	mustWrite(t, filepath.Join(root, "big.txt"), strings.Repeat("a", DefaultLimit+1))
	mustWrite(t, filepath.Join(root, "exact.txt"), strings.Repeat("a", DefaultLimit))
	if err := os.Symlink(filepath.Join("docs", "readme.md"), filepath.Join(root, "link-inside")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link-outside")); err != nil {
		t.Fatalf("symlink outside: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs", "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root, outside
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func resolvedRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	return resolved
}

func TestResolveAcceptsPathsInsideTheRoot(t *testing.T) {
	root, _ := testRoot(t)
	want := filepath.Join(resolvedRoot(t, root), "docs", "readme.md")
	for _, requested := range []string{"docs/readme.md", "./docs/readme.md", "docs//readme.md", "docs/../docs/readme.md", "docs/sub/../readme.md"} {
		got, err := Resolve(root, requested, DefaultLimit)
		if err != nil {
			t.Fatalf("Resolve(%q) rejected an inside path: %v", requested, err)
		}
		if got != want {
			t.Fatalf("Resolve(%q) = %q, want %q", requested, got, want)
		}
	}
}

// A symlink that stays inside the read root is readable; it is not an escape.
func TestResolveAcceptsSymlinkInsideRoot(t *testing.T) {
	root, _ := testRoot(t)
	got, err := Resolve(root, "link-inside", DefaultLimit)
	if err != nil {
		t.Fatalf("inside symlink rejected: %v", err)
	}
	if got != filepath.Join(resolvedRoot(t, root), "docs", "readme.md") {
		t.Fatalf("inside symlink resolved to %q", got)
	}
}

func TestResolveRejections(t *testing.T) {
	root, outside := testRoot(t)
	cases := []struct {
		name      string
		root      string
		requested string
		want      error
	}{
		{"empty path", root, "", ErrPathEmpty},
		{"blank path", root, "   ", ErrPathEmpty},
		{"nul byte", root, "docs/\x00readme.md", ErrPathInvalid},
		{"absolute path", root, "/etc/passwd", ErrPathAbsolute},
		{"absolute path with a double slash", root, "//etc/passwd", ErrPathAbsolute},
		{"parent directory", root, "..", ErrPathEscape},
		{"single level escape", root, "../secret.txt", ErrPathEscape},
		{"deep escape", root, "docs/../../secret.txt", ErrPathEscape},
		{"escape from a sibling directory", root, "docs/sub/../../../secret.txt", ErrPathEscape},
		{"symlink escape", root, "link-outside", ErrSymlinkEscape},
		{"symlink escape below a directory", root, "docs/../link-outside", ErrSymlinkEscape},
		{"missing file", root, "docs/absent.md", ErrNotFound},
		{"directory", root, "docs", ErrNotRegular},
		{"directory reached through a symlink", root, "docs/sub", ErrNotRegular},
		{"over the read limit", root, "big.txt", ErrTooLarge},
		// A root that is not absolute must never act as a prefix for a read.
		{"empty root", "", "passwd", ErrPathOutside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.root, tc.requested, DefaultLimit)
			if err == nil {
				t.Fatalf("Resolve(%q, %q) = %q, want an error", tc.root, tc.requested, got)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Resolve(%q, %q) error = %v, want %v", tc.root, tc.requested, err, tc.want)
			}
			if got != "" {
				t.Fatalf("rejected path returned %q", got)
			}
			// No absolute host path may reach the model through an error string.
			if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), resolvedRoot(t, root)+string(filepath.Separator)) {
				t.Fatalf("error leaks an absolute path: %v", err)
			}
		})
	}
}

func TestResolveHonoursTheSizeCap(t *testing.T) {
	root, _ := testRoot(t)
	if _, err := Resolve(root, "exact.txt", DefaultLimit); err != nil {
		t.Fatalf("a file exactly at the cap must be readable: %v", err)
	}
	_, err := Resolve(root, "big.txt", DefaultLimit)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), "262145") || !strings.Contains(err.Error(), "262144") {
		t.Fatalf("the size error must state the real size and the cap: %v", err)
	}
	if _, err := Resolve(root, "big.txt", DefaultLimit*4); err != nil {
		t.Fatalf("a raised cap must allow the same file: %v", err)
	}
}

func TestWithinRootComparesWholeComponents(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{"/repo", "/repo", true},
		{"/repo", "/repo/docs/a.md", true},
		{"/repo/", "/repo/docs/a.md", true},
		{"/repo", "/repo-other/a.md", false},
		{"/repo", "/repoother", false},
		{"/repo", "/", false},
		{"/repo", "/repo/../etc/passwd", false},
		{"/", "/etc/passwd", true},
		{"/", "/", true},
		{"", "/etc/passwd", false},
		{"repo", "/repo/a.md", false},
		{"/repo", "docs/a.md", false},
	}
	for _, tc := range cases {
		if got := withinRoot(tc.root, tc.path); got != tc.want {
			t.Fatalf("withinRoot(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
}

func TestReadReturnsTextVerbatim(t *testing.T) {
	root, _ := testRoot(t)
	path, err := Resolve(root, "docs/readme.md", DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("Read = %q", got)
	}
}

func TestReadRejectsBinaryContent(t *testing.T) {
	root, _ := testRoot(t)
	// A single NUL byte anywhere in the read window makes the content binary.
	mustWrite(t, filepath.Join(root, "nul.bin"), "plain text\x00then a NUL")
	path, err := Resolve(root, "nul.bin", DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, DefaultLimit); !errors.Is(err, ErrBinary) {
		t.Fatalf("error = %v, want ErrBinary", err)
	}
	// High bytes that are not NUL are still accepted; the criterion is NUL only.
	mustWrite(t, filepath.Join(root, "utf8.txt"), "héllo — 月亮\n")
	path, err = Resolve(root, "utf8.txt", DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Read(path, DefaultLimit); err != nil || got != "héllo — 月亮\n" {
		t.Fatalf("Read = %q, err = %v", got, err)
	}
}

func TestReadEnforcesTheCapWithoutTruncating(t *testing.T) {
	root, _ := testRoot(t)
	path := filepath.Join(root, "big.txt")
	got, err := Read(path, 16)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	if got != "" {
		t.Fatalf("an oversize read must return nothing, got %q", got)
	}
	// Exactly at the cap is allowed: the check refuses more than limit bytes,
	// it does not require less.
	if content, err := Read(path, DefaultLimit+1); err != nil || len(content) != DefaultLimit+1 {
		t.Fatalf("exact cap: len=%d err=%v", len(content), err)
	}
}

func TestReadRejectsDirectoriesAndMissingFiles(t *testing.T) {
	root, _ := testRoot(t)
	if _, err := Read(filepath.Join(root, "docs"), DefaultLimit); err == nil {
		t.Fatal("a directory must not be readable as a file")
	}
	if _, err := Read(filepath.Join(root, "docs", "absent.md"), DefaultLimit); !errors.Is(err, ErrNotReadable) {
		t.Fatalf("missing file error = %v, want ErrNotReadable", err)
	}
}
