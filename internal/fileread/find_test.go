package fileread

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// findRoot builds the tree the find tests walk:
//
//	<root>/docs/guide.md
//	<root>/docs/deep/deep_test.go
//	<root>/engine/engine.go
//	<root>/engine/engine_test.go
//	<root>/engine/large.go        (2 KiB, so a size rendering is observable)
//	<root>/empty.txt
//	<root>/main.go
//	<root>/main.go.bak
//	<root>/main_test.go
//	<root>/notes.md
//	<root>/link-outside -> <outside>/secret.txt
//
// It holds the three shapes a name search has to tell apart: a pattern that
// matches several files at different depths, a name that merely starts with
// another name, and a symbolic link that must not be entered.
func findRoot(t *testing.T) (root, outside string) {
	t.Helper()
	root = t.TempDir()
	outside = t.TempDir()
	for _, path := range []string{
		"docs/guide.md",
		"docs/deep/deep_test.go",
		"engine/engine.go",
		"engine/engine_test.go",
		"empty.txt",
		"main.go",
		"main.go.bak",
		"main_test.go",
		"notes.md",
	} {
		mustWrite(t, filepath.Join(root, path), "content of "+path+"\n")
	}
	mustWrite(t, filepath.Join(outside, "secret_test.go"), "not reachable\n")
	if err := os.Symlink(filepath.Join(outside, "secret_test.go"), filepath.Join(root, "link-outside")); err != nil {
		t.Fatalf("symlink outside: %v", err)
	}
	mustWrite(t, filepath.Join(root, "engine", "large.go"), strings.Repeat("a", 2048))
	return root, outside
}

// findPaths returns the path column of every match line, sorted, so a test
// states which entries matched without depending on walk order. A rendered line
// is "<kind> <size>  <path>", and the fixture holds no name with a space.
func findPaths(t *testing.T, result string) []string {
	t.Helper()
	paths := []string{}
	for _, line := range strings.Split(result, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		kind := fields[0]
		if kind != "dir" && kind != "file" && kind != "link" && kind != "other" {
			continue
		}
		paths = append(paths, fields[len(fields)-1])
	}
	sort.Strings(paths)
	return paths
}

func findHeader(result string) string {
	if index := strings.Index(result, "\n"); index >= 0 {
		return result[:index]
	}
	return result
}

func TestFindMatchesANamePatternAtEveryDepth(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "*_test.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/deep/deep_test.go", "engine/engine_test.go", "main_test.go"}
	if paths := findPaths(t, got); strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("matched %v, want %v\n%s", paths, want, got)
	}
	for _, name := range want {
		if !strings.Contains(got, name) {
			t.Fatalf("result does not name %s:\n%s", name, got)
		}
	}
	if strings.Contains(got, "secret_test.go") {
		t.Fatalf("the walk entered a symbolic link:\n%s", got)
	}
}

// A pattern is anchored to the whole name: it is not a substring test. This is
// the difference that makes "all *_test.go" answerable and keeps an answer of
// "main.go" from silently including main.go.bak.
func TestFindAnchorsThePatternToTheWholeName(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "main.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, got); strings.Join(paths, ",") != "main.go" {
		t.Fatalf("matched %v, want only main.go\n%s", paths, got)
	}
	starred, err := Find(root, "*main*", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"main.go", "main.go.bak", "main_test.go"}
	if paths := findPaths(t, starred); strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("matched %v, want %v\n%s", paths, want, starred)
	}
}

// The pattern language is a glob over one name and nothing more: `*`, `?` and
// character classes are the only things interpreted, so a dot is a dot and a
// regular expression is searched as its own characters.
func TestFindUnderstandsOnlyGlobMetacharacters(t *testing.T) {
	root, _ := findRoot(t)
	dotIsADot, err := Find(root, "m.in.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, dotIsADot); len(paths) != 0 {
		t.Fatalf("a dot was treated as a wildcard: %v\n%s", paths, dotIsADot)
	}
	questionIsOneCharacter, err := Find(root, "main?go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, questionIsOneCharacter); strings.Join(paths, ",") != "main.go" {
		t.Fatalf("? did not match one character: %v\n%s", paths, questionIsOneCharacter)
	}
	classIsAList, err := Find(root, "main[._]*", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"main.go", "main.go.bak", "main_test.go"}
	if paths := findPaths(t, classIsAList); strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("character class matched %v, want %v\n%s", paths, want, classIsAList)
	}
}

func TestFindNamesDirectoriesToo(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "deep", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, got); strings.Join(paths, ",") != "docs/deep/" {
		t.Fatalf("matched %v, want the directory docs/deep/\n%s", paths, got)
	}
	if !strings.Contains(got, "docs/deep/") {
		t.Fatalf("a directory match is not distinguishable from a file:\n%s", got)
	}
	// The whole point of matching a directory is knowing what is below it, so
	// naming it does not end the walk: a deeper name still matches.
	if !strings.Contains(got, "1 path matches") {
		t.Fatalf("header does not state the count:\n%s", got)
	}
}

func TestFindSaysWhenNothingMatched(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "*_spec.rb", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	header := findHeader(got)
	if !strings.Contains(header, "no path matches") {
		t.Fatalf("header = %q", header)
	}
	if !strings.Contains(header, "*_spec.rb") {
		t.Fatalf("header does not name the pattern: %q", header)
	}
	if !strings.Contains(header, "entries examined") {
		t.Fatalf("header does not state what was looked at: %q", header)
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("an empty result carries match lines:\n%s", got)
	}
}

// The starting path keeps the meaning it has everywhere else in the file
// family: one file is looked at, one directory is walked below, and a symbolic
// link is not a start.
func TestFindOfASingleFileLooksAtThatFileName(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(filepath.Join(root, "main_test.go"), "*_test.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, got); strings.Join(paths, ",") != "main_test.go" {
		t.Fatalf("matched %v\n%s", paths, got)
	}
	none, err := Find(filepath.Join(root, "main_test.go"), "main.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, none); len(paths) != 0 {
		t.Fatalf("a file was matched by another file's name: %v\n%s", paths, none)
	}
}

func TestFindRefusesAPathItCannotLookAt(t *testing.T) {
	root, _ := findRoot(t)
	if _, err := Find(filepath.Join(root, "link-outside"), "*", FindOptions{}); !errors.Is(err, ErrNotSearchable) {
		t.Fatalf("symlink start: err = %v, want %v", err, ErrNotSearchable)
	}
	if _, err := Find(filepath.Join(root, "missing"), "*", FindOptions{}); !errors.Is(err, ErrNotSearchable) {
		t.Fatalf("missing start: err = %v, want %v", err, ErrNotSearchable)
	}
}

func TestFindStatesThePathCap(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "*_test.go", FindOptions{MaxPaths: 1})
	if err != nil {
		t.Fatal(err)
	}
	if paths := findPaths(t, got); len(paths) != 1 {
		t.Fatalf("matched %d paths, want exactly 1:\n%s", len(paths), got)
	}
	if !strings.Contains(got, "stopped") || !strings.Contains(got, "not examined") {
		t.Fatalf("result does not state the cap it reached:\n%s", got)
	}
}

func TestFindStatesTheEntryCap(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "*", FindOptions{MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "2 entries examined") {
		t.Fatalf("result does not state how much it looked at:\n%s", got)
	}
	if !strings.Contains(got, "stopped after 2 entries") || !strings.Contains(got, "not examined") {
		t.Fatalf("result does not state the cap it reached:\n%s", got)
	}
}

func TestFindStatesALineCapInsteadOfCuttingAPathSilently(t *testing.T) {
	root, _ := findRoot(t)
	// A name long enough that its rendered path cannot fit, and a cap wide
	// enough to hold the note that says the path was cut.
	name := strings.Repeat("n", 60) + ".txt"
	mustWrite(t, filepath.Join(root, "docs", name), "x\n")
	got, err := Find(root, name, FindOptions{MaxLineBytes: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("a cut path is not marked:\n%s", got)
	}
	if !strings.Contains(got, "it is") {
		t.Fatalf("a cut path does not state its real length:\n%s", got)
	}
}

func TestFindStatesSymbolicLinksItDidNotEnter(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "*", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "symbolic link") {
		t.Fatalf("result does not say a link was left alone:\n%s", got)
	}
	// The link's own name is a real entry, so it is reported; what is never
	// reported is anything behind it.
	if !strings.Contains(got, "link-outside") {
		t.Fatalf("the link itself is not named:\n%s", got)
	}
}

// A find states the same three things about a match as a listing states about
// an entry — what it is, how big it is and where it is — with the path relative
// to where the find started.
func TestFindRendersKindSizeAndRelativePath(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "main.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("result is not a header and one match:\n%s", got)
	}
	// The fixture writes "content of main.go\n", which is 19 bytes.
	if want := "file      19 B  main.go"; lines[1] != want {
		t.Fatalf("match line = %q, want %q", lines[1], want)
	}
}

// The two size renderings are the same measured number in two units, so a
// candidate replacement can differ observably in that one column and nowhere
// else.
func TestFindWithExactBytesChangesOnlyTheUnit(t *testing.T) {
	root, _ := findRoot(t)
	human, err := Find(root, "large.go", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exact, err := Find(root, "large.go", FindOptions{ExactBytes: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human, "2.0 KiB") {
		t.Fatalf("human rendering = %q", human)
	}
	if !strings.Contains(exact, "2048 B") {
		t.Fatalf("exact rendering = %q", exact)
	}
	if strings.Join(findPaths(t, human), ",") != strings.Join(findPaths(t, exact), ",") {
		t.Fatalf("the paths differ: %q vs %q", human, exact)
	}
}

func TestFindFallsBackToTheDefaultCaps(t *testing.T) {
	root, _ := findRoot(t)
	got, err := Find(root, "*", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "stopped") {
		t.Fatalf("a small tree hit a default cap:\n%s", got)
	}
	if DefaultFindPaths <= 0 || DefaultFindEntries <= 0 || DefaultFindLineBytes <= 0 {
		t.Fatalf("defaults are not set: paths=%d entries=%d line=%d", DefaultFindPaths, DefaultFindEntries, DefaultFindLineBytes)
	}
}

func TestFindPatternRejections(t *testing.T) {
	root, _ := findRoot(t)
	cases := []struct {
		pattern string
		want    error
	}{
		{"", ErrPatternEmpty},
		{"   ", ErrPatternEmpty},
		{"docs/guide.md", ErrPatternInvalid},
		{"a/b", ErrPatternInvalid},
		{"[", ErrPatternInvalid},
		{"*_test.go\x00", ErrPatternInvalid},
		{"main\n.go", ErrPatternInvalid},
		{strings.Repeat("a", MaxPatternBytes+1), ErrPatternTooLarge},
	}
	for _, c := range cases {
		if _, err := Find(root, c.pattern, FindOptions{}); !errors.Is(err, c.want) {
			t.Fatalf("Find(%q) error = %v, want %v", c.pattern, err, c.want)
		}
		if err := ValidatePattern(c.pattern); !errors.Is(err, c.want) {
			t.Fatalf("ValidatePattern(%q) error = %v, want %v", c.pattern, err, c.want)
		}
	}
	if err := ValidatePattern("*_test.go"); err != nil {
		t.Fatalf("a good pattern was refused: %v", err)
	}
}

// A refused pattern is refused before any directory is read, so a caller cannot
// pay for a walk it will discard.
func TestFindRefusesAPatternBeforeWalking(t *testing.T) {
	root, _ := findRoot(t)
	if _, err := Find(filepath.Join(root, "missing"), "[", FindOptions{}); !errors.Is(err, ErrPatternInvalid) {
		t.Fatalf("err = %v, want the pattern refusal before the path is even looked at", err)
	}
}
