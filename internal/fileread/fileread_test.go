package fileread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// searchRoot builds a small tree for searching, including every kind of content
// a search has to state something about:
//
//	<root>/a.txt              two lines, both holding the query
//	<root>/docs/keep.md       one line holding the query
//	<root>/sub/deep.txt       one line holding the query
//	<root>/quiet.txt          no query at all
//	<root>/nul.bin            the query, plus a NUL byte
//	<root>/link-inside        -> docs/keep.md
//	<root>/link-outside       -> <outside>/secret.txt, which holds the query
func searchRoot(t *testing.T) (root, outside string) {
	t.Helper()
	root = t.TempDir()
	outside = t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "first line\nErrRPCTimeout here\n")
	mustWrite(t, filepath.Join(root, "docs", "keep.md"), "the ErrRPCTimeout call\n")
	mustWrite(t, filepath.Join(root, "sub", "deep.txt"), "deep ErrRPCTimeout\n")
	mustWrite(t, filepath.Join(root, "quiet.txt"), "nothing to find\n")
	mustWrite(t, filepath.Join(root, "nul.bin"), "ErrRPCTimeout\x00binary\n")
	if err := os.Symlink(filepath.Join("docs", "keep.md"), filepath.Join(root, "link-inside")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	mustWrite(t, filepath.Join(outside, "secret.txt"), "ErrRPCTimeout outside the root\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link-outside")); err != nil {
		t.Fatalf("symlink outside: %v", err)
	}
	return root, outside
}

// searchHits returns the "path:line:" prefix of every match line, so a test can
// assert on what was found and in what order without depending on the text of
// the matched lines.
func searchHits(t *testing.T, result string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	hits := make([]string, 0, len(lines))
	for _, line := range lines[1:] {
		if index := strings.Index(line, ": "); index >= 0 {
			hits = append(hits, line[:index])
		}
	}
	return hits
}

func TestSearchFindsLiteralLinesWithPathAndLineNumber(t *testing.T) {
	root, _ := searchRoot(t)
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The file count is what the search actually searched: the binary file and
	// the two symbolic links are stated separately, so the header never claims a
	// scope it did not cover.
	wantHeader := `3 matches for "ErrRPCTimeout" in 4 files (1 file skipped as binary; 2 symbolic links not followed):`
	if firstLine(got) != wantHeader {
		t.Fatalf("header = %q, want %q:\n%s", firstLine(got), wantHeader, got)
	}
	// The walk is depth-first in path order, and each file's own lines keep
	// their line order, so the hits are ordered by path and then by line.
	want := []string{"a.txt:2", "docs/keep.md:1", "sub/deep.txt:1"}
	hits := searchHits(t, got)
	if len(hits) != len(want) {
		t.Fatalf("hits = %v, want %v:\n%s", hits, want, got)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Fatalf("hits = %v, want %v:\n%s", hits, want, got)
		}
	}
	if !strings.Contains(got, "ErrRPCTimeout here") {
		t.Fatalf("a matched line's text is missing:\n%s", got)
	}
	// The result is deterministic: the same search twice is the same text.
	again, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Fatalf("the same search produced different text:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

func firstLine(s string) string {
	if index := strings.Index(s, "\n"); index >= 0 {
		return s[:index]
	}
	return s
}

// A search matches the literal the model asked for, not a pattern: the query is
// data, so a dot is a dot and a star is a star.
func TestSearchMatchesTheLiteralNotAPattern(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "dotted.txt"), "see docs/a.md for the file\n")
	mustWrite(t, filepath.Join(root, "other.txt"), "see docsXaYmd for the file\n")
	got, err := Search(root, "docs/a.md", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "dotted.txt:1") || strings.Contains(got, "other.txt") {
		t.Fatalf("a literal with a dot must match the literal only:\n%s", got)
	}
	// A regex the model might reach for matches nothing, because a star is a
	// star and there is no pattern language here.
	pattern, err := Search(root, "docs/.*\\.md", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pattern, `no matches for "docs/.*\\.md" in 2 files.`) {
		t.Fatalf("a pattern-looking query must be searched as a literal:\n%s", pattern)
	}
}

func TestSearchSkipsBinaryContentAndCountsIt(t *testing.T) {
	root, _ := searchRoot(t)
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "nul.bin") {
		t.Fatalf("a file with a NUL byte must not be searched:\n%s", got)
	}
	if !strings.Contains(got, "1 file skipped as binary") {
		t.Fatalf("the skipped binary file must be stated:\n%s", got)
	}
}

// A search that reached its match cap says so, and does not pretend to know how
// many matches it did not find.
func TestSearchStatesTheMatchCap(t *testing.T) {
	root, _ := searchRoot(t)
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{MaxMatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(firstLine(got), `1 match for "ErrRPCTimeout" in 2 files`) {
		t.Fatalf("header = %q:\n%s", firstLine(got), got)
	}
	if !strings.Contains(got, "the search stopped at 1 match; the remaining paths were not searched") {
		t.Fatalf("the match cap must be stated as a stop:\n%s", got)
	}
	if hits := searchHits(t, got); len(hits) != 1 {
		t.Fatalf("more matches were rendered than the cap allows: %v", hits)
	}
}

func TestSearchStatesTheFileCap(t *testing.T) {
	root, _ := searchRoot(t)
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{MaxFiles: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "in 1 file") {
		t.Fatalf("the header must count the files actually searched:\n%s", got)
	}
	if !strings.Contains(got, "the search stopped after 1 file; the remaining paths were not searched") {
		t.Fatalf("the file cap must be stated as a stop:\n%s", got)
	}
	if hits := searchHits(t, got); len(hits) != 1 || !strings.HasPrefix(hits[0], "a.txt:") {
		t.Fatalf("hits = %v, want only the first file of the walk:\n%s", hits, got)
	}
	// A cap that covers every file says nothing about one.
	full, err := Search(root, "ErrRPCTimeout", SearchOptions{MaxFiles: 100})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(full, "the search stopped") {
		t.Fatalf("a complete search must not claim it stopped:\n%s", full)
	}
}

// A file larger than the per-file limit is never searched, and the result says
// how many were passed over: a partial read would answer a question about the
// whole file with a statement about its beginning.
func TestSearchStatesThePerFileLimit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "big.txt"), strings.Repeat("x", 100)+" ErrRPCTimeout\n")
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{MaxFileBytes: 40})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `no matches for "ErrRPCTimeout" in 0 files (1 file larger than the 40-byte per-file limit).`) {
		t.Fatalf("an oversize file must be skipped and counted:\n%s", got)
	}
	// The same file is searched once the limit covers it.
	raised, err := Search(root, "ErrRPCTimeout", SearchOptions{MaxFileBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raised, "big.txt:1") {
		t.Fatalf("a raised limit must search the same file:\n%s", raised)
	}
}

// A long matched line is cut to fit the line cap, and the line says it was cut
// and how long it really is.
func TestSearchStatesALineCapInsteadOfCuttingALineSilently(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "long.txt"), strings.Repeat("a", 400)+" ErrRPCTimeout\n")
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{MaxLineBytes: 80})
	if err != nil {
		t.Fatal(err)
	}
	line := firstLine(strings.TrimPrefix(got, firstLine(got)+"\n"))
	if line == "" {
		t.Fatalf("no match line was rendered:\n%s", got)
	}
	if len(line) > 80 {
		t.Fatalf("a match line exceeded the cap: %d bytes %q", len(line), line)
	}
	if !strings.Contains(line, "…(line truncated; it is 414 bytes)") {
		t.Fatalf("a cut line must say it was cut and how long it is: %q", line)
	}
	if !strings.HasPrefix(line, "long.txt:1: ") {
		t.Fatalf("the cut line must still name the file and line: %q", line)
	}
}

// A symbolic link is never followed: not into the root, and above all not out of
// it.
func TestSearchNeverFollowsSymbolicLinks(t *testing.T) {
	root, outside := searchRoot(t)
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "link-inside") || strings.Contains(got, "link-outside") {
		t.Fatalf("a symbolic link must not be searched:\n%s", got)
	}
	if strings.Contains(got, "outside the root") {
		t.Fatalf("content outside the read root was searched:\n%s", got)
	}
	// The link target's content appears once, as the real file it is.
	if strings.Count(got, "the ErrRPCTimeout call") != 1 {
		t.Fatalf("a link target must be searched exactly once, as itself:\n%s", got)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("ErrRPCTimeout only outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again, "only outside") {
		t.Fatalf("changing a file outside the read root changed the search:\n%s", again)
	}
}

func TestSearchSaysWhenNothingWasFound(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "one.txt"), "nothing here\n")
	mustWrite(t, filepath.Join(root, "two.txt"), "nor here\n")
	got, err := Search(root, "a string that is not in the tree", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "no matches for \"a string that is not in the tree\" in 2 files.\n" {
		t.Fatalf("an empty result must say what was searched: %q", got)
	}
	// An empty directory is reported the same way, without inventing a failure.
	empty, err := Search(t.TempDir(), "anything", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if empty != "no matches for \"anything\" in 0 files.\n" {
		t.Fatalf("an empty directory = %q", empty)
	}
}

// A search can start at one file: that is the same walk with only that file in
// it, and the header still says what was searched.
func TestSearchOfAFileSearchesThatFile(t *testing.T) {
	root, _ := searchRoot(t)
	got, err := Search(filepath.Join(root, "a.txt"), "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, `1 match for "ErrRPCTimeout" in 1 file:`) {
		t.Fatalf("header = %q:\n%s", firstLine(got), got)
	}
	// The starting file is named by its base name: the result never carries an
	// absolute host path.
	if !strings.Contains(got, "a.txt:2: ") || strings.Contains(got, root) {
		t.Fatalf("a single-file search must name the file without the host path:\n%s", got)
	}
}

// The plugin is the second line of defence, not the first: a cap the host never
// sent falls back to the package default rather than searching without a bound.
func TestSearchFallsBackToTheDefaultCaps(t *testing.T) {
	root := t.TempDir()
	var content strings.Builder
	for i := 0; i < DefaultSearchMatches+3; i++ {
		content.WriteString("ErrRPCTimeout\n")
	}
	mustWrite(t, filepath.Join(root, "many.txt"), content.String())
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, fmt.Sprintf("the search stopped at %d matches", DefaultSearchMatches)) {
		t.Fatalf("the default match cap was not applied:\n%.200s", got)
	}
	if hits := searchHits(t, got); len(hits) != DefaultSearchMatches {
		t.Fatalf("%d lines were rendered, want the default cap of %d", len(hits), DefaultSearchMatches)
	}
}

func TestSearchQueryRejections(t *testing.T) {
	root, _ := searchRoot(t)
	cases := []struct {
		name  string
		query string
		want  error
	}{
		{"empty", "", ErrQueryEmpty},
		{"blank", "   ", ErrQueryEmpty},
		{"nul byte", "a\x00b", ErrQueryInvalid},
		{"line break", "a\nb", ErrQueryInvalid},
		{"carriage return", "a\rb", ErrQueryInvalid},
		{"too long", strings.Repeat("q", MaxQueryBytes+1), ErrQueryTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Search(root, tc.query, SearchOptions{})
			if err == nil {
				t.Fatalf("Search(%q) = %q, want an error", tc.query, got)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Search(%q) error = %v, want %v", tc.query, err, tc.want)
			}
			if got != "" {
				t.Fatalf("a refused search returned %q", got)
			}
			// A too-long query is the one rejection that has to state the size
			// it refused, so the model can see the limit rather than guess it.
			if errors.Is(err, ErrQueryTooLarge) && !strings.Contains(err.Error(), fmt.Sprintf("%d", MaxQueryBytes)) {
				t.Fatalf("the size error must state the limit: %v", err)
			}
		})
	}
	// A query exactly at the limit is accepted.
	if _, err := Search(root, strings.Repeat("q", MaxQueryBytes), SearchOptions{}); err != nil {
		t.Fatalf("a query at the limit was refused: %v", err)
	}
	if err := ValidateQuery("ErrRPCTimeout"); err != nil {
		t.Fatalf("ValidateQuery rejected a plain literal: %v", err)
	}
}

func TestSearchRefusesAPathItCannotSearch(t *testing.T) {
	root, _ := searchRoot(t)
	if _, err := Search(filepath.Join(root, "absent"), "x", SearchOptions{}); !errors.Is(err, ErrNotSearchable) {
		t.Fatalf("a missing path error = %v, want ErrNotSearchable", err)
	} else if strings.Contains(err.Error(), root) {
		t.Fatalf("the error leaks an absolute path: %v", err)
	}
	// A symbolic link is not a starting point either: the host resolves the path
	// it validated, so a link handed straight to the plugin is refused rather
	// than followed.
	if _, err := Search(filepath.Join(root, "link-outside"), "x", SearchOptions{}); !errors.Is(err, ErrNotSearchable) {
		t.Fatalf("a symbolic link error = %v, want ErrNotSearchable", err)
	}
}

// A directory the walk cannot read is counted, so the result never claims to
// have searched a tree it only walked part of.
func TestSearchCountsADirectoryItCouldNotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a directory whatever its mode says")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "closed", "hidden.txt"), "ErrRPCTimeout\n")
	mustWrite(t, filepath.Join(root, "open.txt"), "ErrRPCTimeout\n")
	if err := os.Chmod(filepath.Join(root, "closed"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "closed"), 0o755) })
	got, err := Search(root, "ErrRPCTimeout", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "1 path could not be read") {
		t.Fatalf("an unreadable directory must be stated:\n%s", got)
	}
	if strings.Contains(got, "hidden.txt") {
		t.Fatalf("a file that could not be read must not be reported as searched:\n%s", got)
	}
}

// ResolveSearch reuses Resolve's resolution and containment, so every refusal a
// read or a listing makes for an unsafe path is made for a search too, and the
// path it accepts is the one a read would accept.
func TestResolveSearchSharesTheSameBoundary(t *testing.T) {
	root, _ := searchRoot(t)
	for _, requested := range []string{".", "docs", "docs/../docs", "a.txt", "docs/keep.md"} {
		got, err := ResolveSearch(root, requested)
		if err != nil {
			t.Fatalf("ResolveSearch(%q) rejected a path inside the root: %v", requested, err)
		}
		if !strings.HasPrefix(got, resolvedRoot(t, root)) {
			t.Fatalf("ResolveSearch(%q) = %q, want a path inside the read root", requested, got)
		}
	}
	// A search may start at anything a listing may start at, and at anything a
	// read may: the resolved path is the read root's real path.
	if got, err := ResolveSearch(root, "docs"); err != nil || got != filepath.Join(resolvedRoot(t, root), "docs") {
		t.Fatalf("ResolveSearch(docs) = %q, err = %v", got, err)
	}
	if got, err := ResolveSearch(root, "docs/keep.md"); err != nil || got != filepath.Join(resolvedRoot(t, root), "docs", "keep.md") {
		t.Fatalf("ResolveSearch(docs/keep.md) = %q, err = %v", got, err)
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
		{"missing path", root, "docs/absent", ErrNotFound},
		{"empty root", "", "docs", ErrPathOutside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveSearch(tc.root, tc.requested)
			if err == nil {
				t.Fatalf("ResolveSearch(%q, %q) = %q, want an error", tc.root, tc.requested, got)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("ResolveSearch(%q, %q) error = %v, want %v", tc.root, tc.requested, err, tc.want)
			}
			if strings.Contains(err.Error(), resolvedRoot(t, root)+string(filepath.Separator)) {
				t.Fatalf("error leaks an absolute path: %v", err)
			}
		})
	}
}

// A path that is neither a regular file nor a directory has nothing to search.
func TestResolveSearchRefusesSomethingThatIsNeitherFileNorDirectory(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	if _, err := ResolveSearch(root, "pipe"); !errors.Is(err, ErrNotSearchable) {
		t.Fatalf("ResolveSearch(pipe) error = %v, want ErrNotSearchable", err)
	}
}

// siblingRoots builds the layout a multi-root call has to keep apart: two
// sibling directories, each holding one file of its own, a file in their parent
// that neither root holds, and a symbolic link inside the first root that leaves
// it for the file in the parent.
//
//	<parent>/a/a.txt        "in a\n"
//	<parent>/a/link-outside -> ../shared.txt
//	<parent>/a/sub/a.txt
//	<parent>/b/b.txt        "in b\n"
//	<parent>/shared.txt     "in parent\n"
func siblingRoots(t *testing.T) (parent, a, b string) {
	t.Helper()
	parent = t.TempDir()
	a = filepath.Join(parent, "a")
	b = filepath.Join(parent, "b")
	mustWrite(t, filepath.Join(a, "a.txt"), "in a\n")
	mustWrite(t, filepath.Join(a, "sub", "a.txt"), "deep in a\n")
	mustWrite(t, filepath.Join(b, "b.txt"), "in b\n")
	mustWrite(t, filepath.Join(parent, "shared.txt"), "in parent\n")
	if err := os.Symlink(filepath.Join("..", "shared.txt"), filepath.Join(a, "link-outside")); err != nil {
		t.Fatalf("symlink outside root a: %v", err)
	}
	return parent, a, b
}

// Every root given to a call is reachable, and the path that is accepted says
// which root accepted it.
func TestResolveInRootsReachesEveryRoot(t *testing.T) {
	_, a, b := siblingRoots(t)
	roots := []string{a, b}
	cases := []struct {
		requested string
		wantRoot  string
		wantPath  string
	}{
		{"a.txt", a, filepath.Join(a, "a.txt")},
		{"sub/a.txt", a, filepath.Join(a, "sub", "a.txt")},
		{"b.txt", b, filepath.Join(b, "b.txt")},
	}
	for _, tc := range cases {
		t.Run(tc.requested, func(t *testing.T) {
			got, err := ResolveInRoots(roots, tc.requested, DefaultLimit)
			if err != nil {
				t.Fatalf("ResolveInRoots(%q) refused a path a root holds: %v", tc.requested, err)
			}
			if got.Root != tc.wantRoot {
				t.Fatalf("ResolveInRoots(%q).Root = %q, want %q", tc.requested, got.Root, tc.wantRoot)
			}
			if got.Path != resolvedPath(t, tc.wantPath) {
				t.Fatalf("ResolveInRoots(%q).Path = %q, want %q", tc.requested, got.Path, resolvedPath(t, tc.wantPath))
			}
		})
	}
	// The order the roots are given in decides which one wins when both hold
	// the name; it never decides whether a root is reachable.
	swapped, err := ResolveInRoots([]string{b, a}, "b.txt", DefaultLimit)
	if err != nil || swapped.Root != b {
		t.Fatalf("ResolveInRoots([b a], b.txt) = %+v, err = %v", swapped, err)
	}
	if list, err := ResolveDirInRoots(roots, "sub"); err != nil || list.Root != a {
		t.Fatalf("ResolveDirInRoots(sub) = %+v, err = %v", list, err)
	}
	// A root is a whole directory, not just the prefix of a path: "." is the
	// root itself, whichever root answers first.
	if list, err := ResolveDirInRoots(roots, "."); err != nil || list.Root != a || list.Path != resolvedPath(t, a) {
		t.Fatalf("ResolveDirInRoots(.) = %+v, err = %v", list, err)
	}
	if search, err := ResolveSearchInRoots(roots, "b.txt"); err != nil || search.Root != b {
		t.Fatalf("ResolveSearchInRoots(b.txt) = %+v, err = %v", search, err)
	}
}

// resolvedPath is the real path of a file the test just built, so an assertion
// compares against what the boundary resolves rather than against the path the
// temporary directory was named with (on macOS /tmp is itself a link).
func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

// A file that only one root holds is reachable when that root is given and
// refused when it is not: the check is per root and is not satisfied by another
// root being present.
func TestResolveInRootsRefusesAFileOnlyAnotherRootHolds(t *testing.T) {
	_, a, b := siblingRoots(t)
	for _, tc := range []struct {
		requested string
		roots     []string
	}{
		{"b.txt", []string{a}},
		{"a.txt", []string{b}},
		{"sub", []string{b}},
	} {
		if got, err := ResolveInRoots(tc.roots, tc.requested, DefaultLimit); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveInRoots(%v, %q) = %+v, err = %v, want ErrNotFound", tc.roots, tc.requested, got, err)
		}
		if got, err := ResolveDirInRoots(tc.roots, tc.requested); err == nil {
			t.Fatalf("ResolveDirInRoots(%v, %q) = %+v, want a refusal", tc.roots, tc.requested, got)
		}
	}
	// The refusals that are not about a missing name are per root too.
	if _, err := ResolveInRoots([]string{b}, "a.txt", DefaultLimit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := ResolveDirInRoots([]string{a}, "a.txt"); !errors.Is(err, ErrNotDir) {
		t.Fatalf("err = %v, want ErrNotDir", err)
	}
}

// A `..` that rises above a root is an escape, and it stays an escape when the
// directory it lands in is another root of the same call. This is the case a
// plain "first root that holds the path wins" loop gets wrong: the requested
// path escapes root a and is refused there, but it is also a legal relative path
// under root b, a sibling directory, so a loop that only keeps trying roots
// would accept it as soon as it reached b — and the answer would depend on the
// order the roots were listed in.
func TestResolveInRootsRefusesAnEscapeIntoASiblingRoot(t *testing.T) {
	_, a, b := siblingRoots(t)
	for _, roots := range [][]string{{a, b}, {b, a}} {
		for _, requested := range []string{"../b/b.txt", filepath.Join("..", filepath.Base(b), "b.txt"), "../shared.txt", "../../etc/passwd", "sub/../../b/b.txt"} {
			if got, err := ResolveInRoots(roots, requested, DefaultLimit); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("ResolveInRoots(%v, %q) = %+v, err = %v, want ErrPathEscape", roots, requested, got, err)
			}
			if _, err := ResolveDirInRoots(roots, requested); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("ResolveDirInRoots(%v, %q) err = %v, want ErrPathEscape", roots, requested, err)
			}
			if _, err := ResolveSearchInRoots(roots, requested); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("ResolveSearchInRoots(%v, %q) err = %v, want ErrPathEscape", roots, requested, err)
			}
		}
	}
	// A `..` that stays inside its root is not an escape, exactly as it is not
	// one for a single-root call.
	if got, err := ResolveInRoots([]string{a, b}, "sub/../a.txt", DefaultLimit); err != nil || got.Root != a {
		t.Fatalf("ResolveInRoots(sub/../a.txt) = %+v, err = %v", got, err)
	}
}

// An absolute path is refused however many roots are given, including one that
// names a file a root really does hold.
func TestResolveInRootsRefusesAbsolutePaths(t *testing.T) {
	_, a, b := siblingRoots(t)
	for _, requested := range []string{"/etc/passwd", filepath.Join(a, "a.txt"), filepath.Join(b, "b.txt")} {
		if got, err := ResolveInRoots([]string{a, b}, requested, DefaultLimit); !errors.Is(err, ErrPathAbsolute) {
			t.Fatalf("ResolveInRoots(%q) = %+v, err = %v, want ErrPathAbsolute", requested, got, err)
		}
	}
	if _, err := ResolveSearchInRoots([]string{a, b}, "/"); !errors.Is(err, ErrPathAbsolute) {
		t.Fatalf("ResolveSearchInRoots(/) err = %v, want ErrPathAbsolute", err)
	}
}

// A symbolic link that leaves the root it sits in is refused, as it is for a
// single root. The refusal is not rescued by another root in the same call: the
// other root is asked about a path inside itself, so nothing outside any root is
// ever reachable.
func TestResolveInRootsRefusesASymlinkThatLeavesARoot(t *testing.T) {
	_, a, b := siblingRoots(t)
	if got, err := ResolveInRoots([]string{a, b}, "link-outside", DefaultLimit); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("ResolveInRoots(link-outside) = %+v, err = %v, want ErrSymlinkEscape", got, err)
	}
	if _, err := ResolveSearchInRoots([]string{a, b}, "link-outside"); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("ResolveSearchInRoots(link-outside) err = %v, want ErrSymlinkEscape", err)
	}
	// The same link name under the other root is a different path, and it is
	// not there at all.
	if _, err := ResolveInRoots([]string{b}, "link-outside", DefaultLimit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// An empty set of roots is a caller mistake and is stated as one: the boundary
// never answers "no roots" by searching anywhere.
func TestResolveInRootsRefusesAnEmptyRootList(t *testing.T) {
	if _, err := ResolveInRoots(nil, "a.txt", DefaultLimit); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("ResolveInRoots(nil) err = %v, want ErrNoRoot", err)
	}
	if _, err := ResolveDirInRoots([]string{}, "."); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("ResolveDirInRoots(empty) err = %v, want ErrNoRoot", err)
	}
	if _, err := ResolveSearchInRoots([]string{}, "a.txt"); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("ResolveSearchInRoots(empty) err = %v, want ErrNoRoot", err)
	}
}
