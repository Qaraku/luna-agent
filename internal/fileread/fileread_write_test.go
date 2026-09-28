package fileread

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustSymlink creates a symbolic link, so a test that needs one says which
// target it names.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// writeTree renders everything below root as one string — relative path, kind,
// and for a symbolic link the target it names. A test compares this before and
// after resolving a path to state that resolving changed nothing on disk, and
// the kind and the link target are part of it because a write that replaced a
// file with a link (or a link with a file) would otherwise go unnoticed.
func writeTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		kind := "other"
		if info, infoErr := entry.Info(); infoErr == nil {
			switch {
			case info.IsDir():
				kind = "dir"
			case info.Mode()&fs.ModeSymlink != 0:
				kind = "link"
			case info.Mode().IsRegular():
				kind = "file"
			}
		}
		line := kind + " " + rel
		if kind == "link" {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			line += " -> " + target
		}
		b.WriteString(line)
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return b.String()
}

// The refusals a write and a read must agree on: the same requested path gets
// the same sentinel from Resolve and from ResolveWrite, because a path a read
// may not reach is not one a write may reach either. The rest of the pre-checks
// are asserted one by one in the write-only table below, since a write accepts
// paths a read refuses (a file that is not there yet) and refuses paths a read
// accepts (a target that is a symbolic link).
func TestResolveWriteSharesTheReadRefusals(t *testing.T) {
	root, _ := testRoot(t)
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
		{"escape from a subdirectory", root, "docs/sub/../../../secret.txt", ErrPathEscape},
		// A root that is not absolute must never act as a prefix.
		{"empty root", "", "passwd", ErrPathOutside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, readErr := Resolve(tc.root, tc.requested, DefaultLimit); !errors.Is(readErr, tc.want) {
				t.Fatalf("Resolve(%q, %q) error = %v, want %v", tc.root, tc.requested, readErr, tc.want)
			}
			got, writeErr := ResolveWrite(tc.root, tc.requested)
			if writeErr == nil {
				t.Fatalf("ResolveWrite(%q, %q) = %+v, want an error", tc.root, tc.requested, got)
			}
			if !errors.Is(writeErr, tc.want) {
				t.Fatalf("ResolveWrite(%q, %q) error = %v, want the read side's %v", tc.root, tc.requested, writeErr, tc.want)
			}
			// A refused write resolves to nothing: there is no path to hand on.
			if got != (Resolved{}) {
				t.Fatalf("a refused ResolveWrite returned %+v", got)
			}
			if strings.Contains(writeErr.Error(), resolvedRoot(t, root)+string(filepath.Separator)) {
				t.Fatalf("the error leaks an absolute path: %v", writeErr)
			}
		})
	}
}

// The paths a write may use: an existing regular file is overwritten and
// anything else that is not there yet is created, and either way the answer is
// the absolute path the write would go to, inside the root the call was given.
func TestResolveWriteAcceptsWhereAWriteWouldGo(t *testing.T) {
	root, _ := testRoot(t)
	resolved := resolvedRoot(t, root)
	cases := []struct {
		name      string
		requested string
		want      string
	}{
		{"a new file in the root", "new.txt", filepath.Join(resolved, "new.txt")},
		{"a new file in a directory", "docs/new.md", filepath.Join(resolved, "docs", "new.md")},
		{"a new file reached through a .. that stays inside", "docs/../new2.txt", filepath.Join(resolved, "new2.txt")},
		{"a new file below a subdirectory", "docs/sub/../new3.md", filepath.Join(resolved, "docs", "new3.md")},
		{"a new file with a doubled separator", "docs//new4.md", filepath.Join(resolved, "docs", "new4.md")},
		{"an existing regular file", "docs/readme.md", filepath.Join(resolved, "docs", "readme.md")},
		{"an existing regular file with a leading ./", "./docs/readme.md", filepath.Join(resolved, "docs", "readme.md")},
		{"an existing regular file through a .. that stays inside", "docs/sub/../readme.md", filepath.Join(resolved, "docs", "readme.md")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveWrite(root, tc.requested)
			if err != nil {
				t.Fatalf("ResolveWrite(%q) refused a path a write may use: %v", tc.requested, err)
			}
			if got.Path != tc.want {
				t.Fatalf("ResolveWrite(%q).Path = %q, want %q", tc.requested, got.Path, tc.want)
			}
			if got.Root != root {
				t.Fatalf("ResolveWrite(%q).Root = %q, want the root as it was given (%q)", tc.requested, got.Root, root)
			}
			if !Within(root, got.Path) {
				t.Fatalf("ResolveWrite(%q).Path = %q is not inside the root", tc.requested, got.Path)
			}
		})
	}
}

// A file that is already there is resolved by the write side exactly as a read
// resolves it: one resolution, so a read and a write cannot be pointed at
// different files by the same requested path.
func TestResolveWriteResolvesAnExistingFileTheWayResolveDoes(t *testing.T) {
	root, _ := testRoot(t)
	for _, requested := range []string{"docs/readme.md", "./docs/readme.md", "docs/../docs/readme.md", "docs/sub/../readme.md", "exact.txt"} {
		read, err := Resolve(root, requested, DefaultLimit)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", requested, err)
		}
		write, err := ResolveWrite(root, requested)
		if err != nil {
			t.Fatalf("ResolveWrite(%q): %v", requested, err)
		}
		if write.Path != read {
			t.Fatalf("ResolveWrite(%q).Path = %q, but Resolve(%q) = %q", requested, write.Path, requested, read)
		}
	}
}

// The refusals that are the write side's own. Where a read of `link-outside`
// reports ErrSymlinkEscape (it followed the link and landed outside the root),
// a write reports ErrWriteTargetSymlink, because what it refuses is the last
// component being a link at all — including a link that points inside the root
// and a link that points at nothing.
func TestResolveWriteRefusals(t *testing.T) {
	root, outside := testRoot(t)
	mustSymlink(t, outside, filepath.Join(root, "link-dir-out"))
	mustSymlink(t, filepath.Join(root, "docs", "missing.md"), filepath.Join(root, "link-dangling"))
	before := writeTree(t, root) + writeTree(t, outside)
	cases := []struct {
		name      string
		requested string
		want      error
	}{
		// The last component has to name a file to write.
		{"an existing directory", "docs", ErrNotRegular},
		{"a subdirectory", "docs/sub", ErrNotRegular},
		{"a trailing separator", "docs/", ErrNotRegular},
		{"a trailing separator on a subdirectory", "docs/sub/", ErrNotRegular},
		{"a trailing separator on a name that is not there", "newdir/", ErrNotRegular},
		{"a trailing separator on a file that is not there", "docs/new.md/", ErrNotRegular},
		{"the root itself", ".", ErrNotRegular},
		{"a directory named by .", "docs/.", ErrNotRegular},
		{"a directory reached by ..", "docs/..", ErrNotRegular},
		{"a .. below a subdirectory", "docs/sub/../..", ErrNotRegular},
		// The parent has to be there, and has to be a directory. Nothing is
		// created to make the path work.
		{"a missing parent directory", "absent/new.md", ErrNotFound},
		{"a missing parent below a subdirectory", "docs/sub/absent/new.md", ErrNotFound},
		{"a parent that is a file", "docs/readme.md/sub.md", ErrNotDir},
		// The parent resolves inside the root, or the write is refused.
		{"a new file below a directory link out of the root", "link-dir-out/new.md", ErrSymlinkEscape},
		// The target itself may not be a link, whatever it points at.
		{"a link to a file inside the root", "link-inside", ErrWriteTargetSymlink},
		{"a link to a file outside the root", "link-outside", ErrWriteTargetSymlink},
		{"a link reached through a .. that stays inside", "docs/../link-outside", ErrWriteTargetSymlink},
		{"a dangling link", "link-dangling", ErrWriteTargetSymlink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveWrite(root, tc.requested)
			if err == nil {
				t.Fatalf("ResolveWrite(%q) = %+v, want an error", tc.requested, got)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("ResolveWrite(%q) error = %v, want %v", tc.requested, err, tc.want)
			}
			if got != (Resolved{}) {
				t.Fatalf("a refused ResolveWrite returned %+v", got)
			}
			if strings.Contains(err.Error(), resolvedRoot(t, root)+string(filepath.Separator)) {
				t.Fatalf("the error leaks an absolute path: %v", err)
			}
		})
	}
	if after := writeTree(t, root) + writeTree(t, outside); after != before {
		t.Fatalf("a refusal changed the tree:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Resolving a path is not writing it: the answer may be the path of a file that
// does not exist, and the file is still not there afterwards. The whole tree is
// compared, and the missing parent directory is checked separately, because
// "created no directories" is one of the things a resolver must not do.
func TestResolveWriteCreatesNothing(t *testing.T) {
	root, _ := testRoot(t)
	before := writeTree(t, root)
	for _, requested := range []string{"new.txt", "docs/new.md", "docs/sub/new.md", "shared/new/deep/deeper.md"} {
		got, err := ResolveWrite(root, requested)
		if err == nil {
			if _, statErr := os.Lstat(got.Path); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("ResolveWrite(%q) answered %q, which is on disk: %v", requested, got.Path, statErr)
			}
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveWrite(%q) error = %v, want the missing parent's ErrNotFound", requested, err)
		}
	}
	if after := writeTree(t, root); after != before {
		t.Fatalf("resolving changed the tree:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, dir := range []string{"shared", filepath.Join("docs", "sub", "absent")} {
		if _, err := os.Stat(filepath.Join(root, dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%q was created: %v", dir, err)
		}
	}
}

// The answer for a file that is already there names that file: the caller can
// overwrite it, and it is still the file the test wrote — resolving does not
// write.
func TestResolveWriteNamesTheExistingFileItWouldOverwrite(t *testing.T) {
	root, _ := testRoot(t)
	got, err := ResolveWrite(root, "docs/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(got.Path)
	if err != nil {
		t.Fatalf("the resolved path cannot be looked at: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("the resolved path is %v, want a regular file", info.Mode())
	}
	want, err := os.Stat(filepath.Join(root, "docs", "readme.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, want) {
		t.Fatalf("ResolveWrite(docs/readme.md) = %q, which is not the file the test wrote", got.Path)
	}
	if data, err := os.ReadFile(got.Path); err != nil || string(data) != "hello\n" {
		t.Fatalf("the file changed: %q, err = %v", data, err)
	}
}

// A target that is a symbolic link is refused, and the link is left as it was:
// the refusal is about the name, not about where the link points, so it does not
// replace the link and does not follow it either.
func TestResolveWriteRefusesASymlinkTargetAndLeavesItAlone(t *testing.T) {
	root, outside := testRoot(t)
	mustSymlink(t, filepath.Join(root, "docs", "missing.md"), filepath.Join(root, "link-dangling"))
	insideTarget := filepath.Join("docs", "readme.md")
	for _, requested := range []string{"link-inside", "link-outside", "link-dangling"} {
		got, err := ResolveWrite(root, requested)
		if !errors.Is(err, ErrWriteTargetSymlink) {
			t.Fatalf("ResolveWrite(%q) = %+v, err = %v, want ErrWriteTargetSymlink", requested, got, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, "link-inside")); err != nil || target != insideTarget {
		t.Fatalf("link-inside = %q, err = %v, want the link it was (%q)", target, err, insideTarget)
	}
	if info, err := os.Lstat(filepath.Join(root, "link-inside")); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link-inside is no longer a symbolic link: %v, err = %v", info, err)
	}
	if secret, err := os.ReadFile(filepath.Join(outside, "secret.txt")); err != nil || string(secret) != "secret\n" {
		t.Fatalf("the file the outside link points at was changed: %q, err = %v", secret, err)
	}
	// The link that points inside the root is refused too, even though the read
	// side accepts it: what a write is told is the name it would replace.
	if _, err := Resolve(root, "link-inside", DefaultLimit); err != nil {
		t.Fatalf("the read side accepted link-inside before the write side refused it: %v", err)
	}
}

// A symbolic link is resolved for every component except the last one, so a
// directory link that stays inside the root is a legal place to create a file —
// the answer is the real directory — while a directory link that leaves the root
// is refused before anything is created.
func TestResolveWriteFollowsADirectoryLinkOnlyInsideTheRoot(t *testing.T) {
	root, outside := testRoot(t)
	mustSymlink(t, filepath.Join(root, "docs"), filepath.Join(root, "link-dir-in"))
	mustSymlink(t, outside, filepath.Join(root, "link-dir-out"))
	got, err := ResolveWrite(root, "link-dir-in/new.md")
	if err != nil {
		t.Fatalf("a directory link inside the root was refused: %v", err)
	}
	if want := filepath.Join(resolvedRoot(t, root), "docs", "new.md"); got.Path != want {
		t.Fatalf("ResolveWrite(link-dir-in/new.md).Path = %q, want the real path %q", got.Path, want)
	}
	if _, err := os.Lstat(got.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the resolved path is already on disk: %v", err)
	}
	if got, err := ResolveWrite(root, "link-dir-out/new.md"); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("ResolveWrite(link-dir-out/new.md) = %+v, err = %v, want ErrSymlinkEscape", got, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a file was created outside the root: %v", err)
	}
}

// The first root that admits the path wins, and the answer says which one it
// was. A write differs from a read here in one way worth stating: a name no root
// holds yet is not a refusal, it is a new file in the first root that can take
// it — which is exactly why the order the roots are given in decides.
func TestResolveWriteInRootsPicksTheFirstRootThatAdmitsThePath(t *testing.T) {
	_, a, b := siblingRoots(t)
	ra, rb := resolvedRoot(t, a), resolvedRoot(t, b)
	cases := []struct {
		name      string
		roots     []string
		requested string
		wantRoot  string
		wantPath  string
	}{
		{"an existing file in the first root", []string{a, b}, "a.txt", a, filepath.Join(ra, "a.txt")},
		{"an existing file in the second root, asked second", []string{b, a}, "b.txt", b, filepath.Join(rb, "b.txt")},
		{"a name the root does not hold yet is a new file there", []string{a}, "b.txt", a, filepath.Join(ra, "b.txt")},
		{"the first root takes a name only the second holds", []string{a, b}, "b.txt", a, filepath.Join(ra, "b.txt")},
		{"the order decides a name both roots would take", []string{b, a}, "a.txt", b, filepath.Join(rb, "a.txt")},
		{"a new file in a directory only one root holds", []string{b, a}, "sub/b.txt", a, filepath.Join(ra, "sub", "b.txt")},
		{"a new file in the first root", []string{a, b}, "new.txt", a, filepath.Join(ra, "new.txt")},
	}
	before := writeTree(t, a) + writeTree(t, b)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveWriteInRoots(tc.roots, tc.requested)
			if err != nil {
				t.Fatalf("ResolveWriteInRoots(%v, %q) refused a path a root admits: %v", tc.roots, tc.requested, err)
			}
			if got.Root != tc.wantRoot {
				t.Fatalf("ResolveWriteInRoots(%v, %q).Root = %q, want %q", tc.roots, tc.requested, got.Root, tc.wantRoot)
			}
			if got.Path != tc.wantPath {
				t.Fatalf("ResolveWriteInRoots(%v, %q).Path = %q, want %q", tc.roots, tc.requested, got.Path, tc.wantPath)
			}
		})
	}
	if after := writeTree(t, a) + writeTree(t, b); after != before {
		t.Fatalf("resolving changed a root:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A `..` that rises above a root is an escape and stays one when the directory
// it lands in is another root of the same call, exactly as it does for a read.
// A sibling root never rescues it, in either order, and nothing is written on
// the way to the refusal.
func TestResolveWriteInRootsRefusesAnEscapeIntoASiblingRoot(t *testing.T) {
	parent, a, b := siblingRoots(t)
	before := writeTree(t, a) + writeTree(t, b) + writeTree(t, parent)
	for _, roots := range [][]string{{a, b}, {b, a}} {
		for _, requested := range []string{"../b/new.txt", "../b/b.txt", "../shared.txt", "sub/../../b/new.txt", "../../etc/passwd"} {
			got, err := ResolveWriteInRoots(roots, requested)
			if !errors.Is(err, ErrPathEscape) {
				t.Fatalf("ResolveWriteInRoots(%v, %q) = %+v, err = %v, want ErrPathEscape", roots, requested, got, err)
			}
			// The read side refuses the same path for the same reason: the
			// climb is decided before any root is asked.
			if _, readErr := ResolveInRoots(roots, requested, DefaultLimit); !errors.Is(readErr, ErrPathEscape) {
				t.Fatalf("ResolveInRoots(%v, %q) err = %v, want ErrPathEscape", roots, requested, readErr)
			}
			if _, listErr := ResolveDirInRoots(roots, requested); !errors.Is(listErr, ErrPathEscape) {
				t.Fatalf("ResolveDirInRoots(%v, %q) err = %v, want ErrPathEscape", roots, requested, listErr)
			}
		}
	}
	if after := writeTree(t, a) + writeTree(t, b) + writeTree(t, parent); after != before {
		t.Fatalf("an escape changed the tree:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A refusal that belongs to one root is moved past, so a name that is a link out
// of one root is a new file in another — but never a write through that link. A
// refusal every root makes is the answer, and no root's refusal is answered by
// reaching the file another root's link points at.
func TestResolveWriteInRootsMovesPastARefusalOnlyOneRootMakes(t *testing.T) {
	parent, a, b := siblingRoots(t)
	got, err := ResolveWriteInRoots([]string{a, b}, "link-outside")
	if err != nil {
		t.Fatalf("ResolveWriteInRoots(link-outside) refused a name the second root can take: %v", err)
	}
	if got.Root != b || got.Path != filepath.Join(resolvedRoot(t, b), "link-outside") {
		t.Fatalf("ResolveWriteInRoots(link-outside) = %+v, want a new file inside %q", got, b)
	}
	if _, err := os.Lstat(got.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the resolved path is already on disk: %v", err)
	}
	if _, err := ResolveWriteInRoots([]string{a}, "link-outside"); !errors.Is(err, ErrWriteTargetSymlink) {
		t.Fatalf("with only the root that holds the link, err = %v, want ErrWriteTargetSymlink", err)
	}
	// With a link of the same name in both roots, every root refuses, and the
	// refusal is what comes back rather than a write somewhere.
	mustSymlink(t, filepath.Join("..", "shared.txt"), filepath.Join(b, "link-outside"))
	for _, roots := range [][]string{{a, b}, {b, a}} {
		if _, err := ResolveWriteInRoots(roots, "link-outside"); !errors.Is(err, ErrWriteTargetSymlink) {
			t.Fatalf("ResolveWriteInRoots(%v, link-outside) err = %v, want ErrWriteTargetSymlink", roots, err)
		}
	}
	if shared, err := os.ReadFile(filepath.Join(parent, "shared.txt")); err != nil || string(shared) != "in parent\n" {
		t.Fatalf("the file the links point at was changed: %q, err = %v", shared, err)
	}
	if target, err := os.Readlink(filepath.Join(a, "link-outside")); err != nil || target != filepath.Join("..", "shared.txt") {
		t.Fatalf("the link in the first root = %q, err = %v, want the link it was", target, err)
	}
}

// An empty set of roots is a caller mistake, and the pre-checks a path is
// refused by are made for a write as they are for a read.
func TestResolveWriteInRootsRefusesAnEmptyRootListLikeTheReadSide(t *testing.T) {
	_, a, b := siblingRoots(t)
	if got, err := ResolveWriteInRoots(nil, "new.txt"); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("ResolveWriteInRoots(nil) = %+v, err = %v, want ErrNoRoot", got, err)
	}
	if _, err := ResolveWriteInRoots([]string{}, "."); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("ResolveWriteInRoots([]) err = %v, want ErrNoRoot", err)
	}
	for _, requested := range []string{"", "   ", "docs\x00", "/etc"} {
		_, writeErr := ResolveWriteInRoots([]string{a, b}, requested)
		_, readErr := ResolveInRoots([]string{a, b}, requested, DefaultLimit)
		if writeErr == nil || readErr == nil {
			t.Fatalf("ResolveWriteInRoots(%q) err = %v and ResolveInRoots err = %v, want a refusal from both", requested, writeErr, readErr)
		}
		if writeErr.Error() != readErr.Error() {
			t.Fatalf("the two sides refuse %q differently: write %v, read %v", requested, writeErr, readErr)
		}
	}
}

// Within is the rule the boundary is decided by, exported: whole components, not
// a prefix of characters, so `…/repo-other` is not inside `…/repo`.
func TestWithinExportsTheWholeComponentRule(t *testing.T) {
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
		if got := Within(tc.root, tc.path); got != tc.want {
			t.Fatalf("Within(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
		// The exported rule is the rule the boundary uses, not a second copy of
		// it that could drift away.
		if got := withinRoot(tc.root, tc.path); got != tc.want {
			t.Fatalf("withinRoot(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
	// Two real sibling directories: exactly one of them holds the file, and the
	// other one is not treated as inside it however the paths are written.
	_, a, b := siblingRoots(t)
	ra, rb := resolvedRoot(t, a), resolvedRoot(t, b)
	file := filepath.Join(a, "a.txt")
	for _, tc := range []struct {
		root, path string
		want       bool
	}{
		{a, file, true},
		{a, a, true},
		{ra, ra, true},
		{rb, ra, false},
		{b, file, false},
		{b, a, false},
		{ra, filepath.Join(ra, "a.txt"), true},
	} {
		if got := Within(tc.root, tc.path); got != tc.want {
			t.Fatalf("Within(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
}
