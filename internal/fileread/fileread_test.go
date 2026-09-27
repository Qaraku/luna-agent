package fileread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

// ResolveDir shares Resolve's resolution and containment, so every refusal a
// read makes for an unsafe path is made for a listing too — and a path that is
// not a directory is refused where a read would accept a regular file.
func TestResolveDirAcceptsDirectoriesInsideTheRootAndRejectsTheRest(t *testing.T) {
	root, _ := testRoot(t)
	wantResolved := filepath.Join(resolvedRoot(t, root), "docs")
	for _, requested := range []string{"docs", "./docs", "docs/", "docs/sub/.."} {
		got, err := ResolveDir(root, requested)
		if err != nil {
			t.Fatalf("ResolveDir(%q) rejected a directory inside the root: %v", requested, err)
		}
		if got != wantResolved {
			t.Fatalf("ResolveDir(%q) = %q, want %q", requested, got, wantResolved)
		}
	}
	// The read root itself is listable: "." is the read root, and so is an empty
	// relative climb that lands on it.
	for _, requested := range []string{".", "./", "docs/.."} {
		got, err := ResolveDir(root, requested)
		if err != nil {
			t.Fatalf("ResolveDir(%q) rejected the read root: %v", requested, err)
		}
		if got != resolvedRoot(t, root) {
			t.Fatalf("ResolveDir(%q) = %q, want the read root", requested, got)
		}
	}

	cases := []struct {
		name      string
		root      string
		requested string
		want      error
	}{
		{"empty path", root, "", ErrPathEmpty},
		{"blank path", root, "   ", ErrPathEmpty},
		{"nul byte", root, "docs\x00", ErrPathInvalid},
		{"absolute path", root, "/etc", ErrPathAbsolute},
		{"parent directory", root, "..", ErrPathEscape},
		{"deep escape", root, "docs/../../etc", ErrPathEscape},
		{"symlink escape", root, "link-outside", ErrSymlinkEscape},
		{"missing directory", root, "docs/absent", ErrNotFound},
		{"a regular file", root, "docs/readme.md", ErrNotDir},
		{"a symlink to a file", root, "link-inside", ErrNotDir},
		{"empty root", "", "docs", ErrPathOutside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDir(tc.root, tc.requested)
			if err == nil {
				t.Fatalf("ResolveDir(%q, %q) = %q, want an error", tc.root, tc.requested, got)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("ResolveDir(%q, %q) error = %v, want %v", tc.root, tc.requested, err, tc.want)
			}
			if got != "" {
				t.Fatalf("a rejected path returned %q", got)
			}
			// No absolute host path may reach the model through an error string.
			if strings.Contains(err.Error(), resolvedRoot(t, root)+string(filepath.Separator)) {
				t.Fatalf("error leaks an absolute path: %v", err)
			}
		})
	}
	// A symlink to a directory inside the root is listable, and resolves to the
	// directory it points at: containment is decided on the real path.
	if err := os.Symlink("docs", filepath.Join(root, "docs-link")); err != nil {
		t.Fatalf("symlink to a directory: %v", err)
	}
	got, err := ResolveDir(root, "docs-link")
	if err != nil {
		t.Fatalf("inside symlink to a directory rejected: %v", err)
	}
	if got != wantResolved {
		t.Fatalf("ResolveDir(docs-link) = %q, want %q", got, wantResolved)
	}
}

// listRoot builds a directory holding every kind a listing has to state
// correctly, and returns its path.
func listRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "readme.md"), "hello\n")
	mustWrite(t, filepath.Join(root, "big.txt"), strings.Repeat("a", 1536))
	mustWrite(t, filepath.Join(root, "docs", "readme.md"), "hello\n")
	mustWrite(t, filepath.Join(root, "notes"), "a file named like a directory\n")
	if err := os.Symlink("readme.md", filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return root
}

func TestListNamesEveryEntryWithItsKindAndSize(t *testing.T) {
	root := listRoot(t)
	got, err := List(root, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("listing has %d lines, want a header and five entries:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "5 entries: ") || !strings.Contains(lines[0], "1 dir") || !strings.Contains(lines[0], "3 files") || !strings.Contains(lines[0], "1 link") {
		t.Fatalf("the header must state what was found:\n%s", got)
	}
	// Directories first, then files and links, each group by name.
	want := []struct{ kind, name string }{
		{"dir", "docs"},
		{"file", "big.txt"},
		{"file", "notes"},
		{"file", "readme.md"},
	}
	for i, entry := range want {
		line := lines[i+1]
		if !strings.HasPrefix(line, entry.kind) || !strings.HasSuffix(line, entry.name) {
			t.Fatalf("line %d = %q, want a %s named %q:\n%s", i+1, line, entry.kind, entry.name, got)
		}
	}
	// The link is reported as a link and never followed, so it carries no size
	// and the listing never mentions what it points at.
	for _, line := range lines[1:] {
		if strings.HasSuffix(line, "link") {
			if !strings.HasPrefix(line, "link") || !strings.Contains(line, "-") {
				t.Fatalf("a link must be named as a link without a size: %q", line)
			}
		}
	}
	// Sizes are real: 1536 bytes is not 1.5 KiB by accident.
	if !strings.Contains(got, "1.5 KiB") {
		t.Fatalf("the size of big.txt is not rendered:\n%s", got)
	}
	if !strings.Contains(got, "6 B") {
		t.Fatalf("the size of a small file is not rendered:\n%s", got)
	}
}

func TestListWithExactBytesChangesOnlyTheUnit(t *testing.T) {
	root := listRoot(t)
	human, err := List(root, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exact, err := List(root, ListOptions{ExactBytes: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human, "1.5 KiB") || strings.Contains(exact, "KiB") {
		t.Fatalf("the two renderings must differ in the unit only:\nhuman:\n%s\nexact:\n%s", human, exact)
	}
	if !strings.Contains(exact, "1536 B") {
		t.Fatalf("the exact rendering must state the measured bytes:\n%s", exact)
	}
}

// A listing is capped, and a cap it reached is stated: a directory that has more
// entries than one listing renders says how many were left out instead of
// dropping them silently.
func TestListStatesTheEntryCapInsteadOfTruncatingSilently(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		mustWrite(t, filepath.Join(root, name), "x")
	}
	got, err := List(root, ListOptions{MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "5 entries") {
		t.Fatalf("the header must report the real number of entries:\n%s", got)
	}
	if !strings.Contains(got, "3 entries are not listed: one listing returns at most 2 entries") {
		t.Fatalf("the cap must be stated explicitly:\n%s", got)
	}
	if strings.Count(got, ".txt") != 2 {
		t.Fatalf("more entries were rendered than the cap allows:\n%s", got)
	}
	// A cap that covers every entry says nothing about one.
	full, err := List(root, ListOptions{MaxEntries: 5})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(full, "not listed") {
		t.Fatalf("a complete listing must not claim entries were left out:\n%s", full)
	}
}

// A long name is cut to fit the line cap, and the line says it was cut and how
// long the real name is, so the listing never overstates what it shows.
func TestListStatesALineCapInsteadOfCuttingANameSilently(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("n", 240)
	mustWrite(t, filepath.Join(root, long), "x")
	got, err := List(root, ListOptions{MaxLineBytes: 80})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	entry := lines[len(lines)-1]
	if len(entry) > 80 {
		t.Fatalf("a line exceeded the cap: %d bytes %q", len(entry), entry)
	}
	if !strings.Contains(entry, "name truncated; it is 240 bytes") {
		t.Fatalf("a cut name must say it was cut and how long it is: %q", entry)
	}
	if !strings.HasPrefix(entry, "file") {
		t.Fatalf("the cut line must still state the kind: %q", entry)
	}
	// A name that fits is never touched, and a cap large enough for the name
	// leaves it whole.
	if !strings.Contains(entry, strings.Repeat("n", 20)) {
		t.Fatalf("the cut must keep as much of the name as the cap allows: %q", entry)
	}
	whole, err := List(root, ListOptions{MaxLineBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(whole, long) || strings.Contains(whole, "truncated") {
		t.Fatalf("a name inside the cap must be rendered whole:\n%s", whole)
	}
	// The cut never splits a rune: a multi-byte name truncated at a byte
	// boundary stays valid UTF-8.
	root2 := t.TempDir()
	mustWrite(t, filepath.Join(root2, strings.Repeat("月亮", 40)), "x")
	got2, err := List(root2, ListOptions{MaxLineBytes: 40})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(got2, "\n"), "\n") {
		if !utf8.ValidString(line) {
			t.Fatalf("a cut line is not valid UTF-8: %q", line)
		}
	}
}

func TestListOfAnEmptyDirectorySaysSo(t *testing.T) {
	got, err := List(t.TempDir(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "0 entries: the directory is empty\n" {
		t.Fatalf("empty listing = %q", got)
	}
}

func TestListRefusesAPathItCannotRead(t *testing.T) {
	root := listRoot(t)
	// A file is not a directory, and the error names no absolute path.
	if _, err := List(filepath.Join(root, "readme.md"), ListOptions{}); !errors.Is(err, ErrNotListable) {
		t.Fatalf("listing a file error = %v, want ErrNotListable", err)
	} else if strings.Contains(err.Error(), root) {
		t.Fatalf("the error leaks an absolute path: %v", err)
	}
	if _, err := List(filepath.Join(root, "absent"), ListOptions{}); !errors.Is(err, ErrNotListable) {
		t.Fatalf("listing a missing directory error = %v, want ErrNotListable", err)
	}
}

// The plugin is the second line of defence, not the first: a cap the host never
// sent falls back to the package default rather than rendering everything.
func TestListFallsBackToTheDefaultCaps(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < DefaultListEntries+5; i++ {
		mustWrite(t, filepath.Join(root, fmt.Sprintf("f%04d.txt", i)), "x")
	}
	got, err := List(root, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, fmt.Sprintf("%d entries are not listed", 5)) {
		t.Fatalf("the default entry cap was not applied:\n%.200s", got)
	}
}
