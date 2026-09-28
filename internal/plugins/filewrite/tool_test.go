package filewrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/settings"
)

// fixture is one test's world: a working directory, the settings file that says
// which directories Luna may write in, and a tool that reads both.
//
// There is no settings file until a test allows a directory, because that is the
// state of an installation where nobody has allowed anything — and it is not the
// same state as a file that exists and allows nothing, which is a case of its
// own.
type fixture struct {
	// parent holds the working directory and whatever else a test puts beside
	// it. It is resolved, because the write boundary resolves symbolic links and
	// compares real paths.
	parent string
	// root is this run's working directory: <parent>/work.
	root string
	// settingsPath is the settings file the tool reads.
	settingsPath string
	tool         *WriteTool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	parent := resolved(t, t.TempDir())
	root := filepath.Join(parent, "work")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create the working directory: %v", err)
	}
	settingsPath := filepath.Join(resolved(t, t.TempDir()), settings.FileName)
	return &fixture{parent: parent, root: root, settingsPath: settingsPath, tool: NewWriteTool(settingsPath)}
}

// resolved returns the real path of dir. The boundary resolves symbolic links,
// so a fixture that compared against an unresolved temp directory would refuse
// its own working directory on a machine where /tmp is a link.
func resolved(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

// allow writes the settings file the way the settings page does: through the
// settings package's own writer, with the directories given marked writable.
// Called with no directories it writes a file that allows nothing.
func (f *fixture) allow(t *testing.T, dirs ...string) {
	t.Helper()
	file := settings.Settings{}
	for _, dir := range dirs {
		file = file.WithWriteDir(dir, true)
	}
	if err := settings.Save(f.settingsPath, file); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

// breakSettings replaces the settings file with a directory, so reading it
// fails the way an unreadable file does.
func (f *fixture) breakSettings(t *testing.T) {
	t.Helper()
	if err := os.Remove(f.settingsPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove settings: %v", err)
	}
	if err := os.Mkdir(f.settingsPath, 0o700); err != nil {
		t.Fatalf("make the settings file unreadable: %v", err)
	}
}

// call runs the tool in a run whose working directory is this fixture's.
func (f *fixture) call(t *testing.T, arguments string) (string, error) {
	t.Helper()
	return f.tool.Invoke(plugin.WithRoots(context.Background(), []string{f.root}), arguments)
}

// callIn runs the tool in a run whose working directories are exactly these; an
// empty (but non-nil) list is a run with no working directory at all.
func (f *fixture) callIn(t *testing.T, roots []string, arguments string) (string, error) {
	t.Helper()
	return f.tool.Invoke(plugin.WithRoots(context.Background(), roots), arguments)
}

// write puts a file under the working directory, creating its directory, and
// returns its absolute path.
func (f *fixture) write(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	// The mode is set explicitly because Create's mode is filtered by the
	// process umask, and a fixture whose files come out 0640 would make every
	// assertion about the permission bits a statement about this machine.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod %s: %v", rel, err)
	}
	return path
}

// read returns what is on disk at rel.
func (f *fixture) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// modeOf returns the permission bits on disk at rel.
func (f *fixture) modeOf(t *testing.T, rel string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	return info.Mode().Perm()
}

// arguments renders a call the way the model sends one.
func arguments(t *testing.T, path, content string, createOnly bool) string {
	t.Helper()
	payload := map[string]any{"path": path, "content": content}
	if createOnly {
		payload["create_only"] = true
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal arguments: %v", err)
	}
	return string(data)
}

// snapshot records every path below dir with its kind, permission bits,
// modification time, size and contents. A test that claims "nothing was written"
// compares a snapshot taken before the call with one taken after it, so the
// claim covers the whole tree instead of the one file the test thought of — and
// it covers the modification time, which is what a rewrite with identical
// contents would move.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		detail := ""
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			detail = "-> " + target
		case entry.IsDir():
		default:
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				// A file a test made unreadable is recorded as unreadable
				// rather than ending the snapshot: its mode and time are still
				// part of what must not change.
				detail = fmt.Sprintf("<unreadable: %v>", readErr)
			} else {
				detail = string(data)
			}
		}
		out[rel] = fmt.Sprintf("%s|%d|%s", info.Mode(), info.ModTime().UnixNano(), detail)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", filepath.Base(dir), err)
	}
	return out
}

// mustBeUnchanged compares a snapshot with the tree as it is now, and names the
// first path that moved so a failure says what changed rather than only that
// something did.
func mustBeUnchanged(t *testing.T, before map[string]string, dir string) {
	t.Helper()
	after := snapshot(t, dir)
	for path, was := range before {
		now, ok := after[path]
		if !ok {
			t.Fatalf("%q was removed by the call", path)
		}
		if now != was {
			t.Fatalf("%q changed on disk:\nbefore=%s\nafter= %s", path, was, now)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Fatalf("%q appeared on disk", path)
		}
	}
}

// Every refusal is one call in a world prepared for it, and every case is
// checked the same way: the call is refused with no result, the refusal says
// what the model needs in order to tell the user what to do, it is a call-level
// refusal rather than an infrastructure failure, and nothing anywhere under the
// fixture's parent changed — not the target, not a directory beside it, not a
// modification time.
func TestARefusedCallWritesNothingAndSaysWhy(t *testing.T) {
	cases := []struct {
		name string
		// world builds the fixture and prepares the working directory. What it
		// puts on disk is snapshotted before the call and compared after it.
		world func(t *testing.T) *fixture
		// call is the one call the case makes.
		call func(t *testing.T, f *fixture) (string, error)
		// want are substrings the refusal has to carry.
		want []string
	}{
		{
			name:  "no settings file at all",
			world: func(t *testing.T) *fixture { return newFixture(t) },
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "hello\n", false))
			},
			want: []string{
				`"notes.md" is not inside a directory Luna may write in`,
				"no directory has been allowed",
				"settings page",
				"工作区",
			},
		},
		{
			name: "a settings file that allows nothing",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "hello\n", false))
			},
			want: []string{"no directory has been allowed", "setting"},
		},
		{
			name: "the working directory is not among the allowed ones",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				other := filepath.Join(f.parent, "other")
				if err := os.Mkdir(other, 0o755); err != nil {
					t.Fatalf("mkdir other: %v", err)
				}
				f.allow(t, other)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "hello\n", false))
			},
			want: []string{`"notes.md" is not inside a directory the user allowed`, "settings page"},
		},
		{
			name: "an allowed directory outside this run's working directories",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				other := filepath.Join(f.parent, "other")
				if err := os.Mkdir(other, 0o755); err != nil {
					t.Fatalf("mkdir other: %v", err)
				}
				if err := os.WriteFile(filepath.Join(other, "kept.md"), []byte("kept\n"), 0o644); err != nil {
					t.Fatalf("write other/kept.md: %v", err)
				}
				f.allow(t, other)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				// The name exists in a directory the user allowed, and the run
				// still must not reach it: a grant narrows the run's working
				// directories and never widens them.
				return f.call(t, arguments(t, "../other/kept.md", "gone\n", false))
			},
			want: []string{"escapes"},
		},
		{
			name: "a path that climbs out of the working directory",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				if err := os.Mkdir(filepath.Join(f.parent, "outside"), 0o755); err != nil {
					t.Fatalf("mkdir outside: %v", err)
				}
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "../outside/notes.md", "hello\n", false))
			},
			want: []string{"escapes"},
		},
		{
			name: "an absolute path",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "/etc/notes.md", "hello\n", false))
			},
			want: []string{"absolute paths are not allowed"},
		},
		{
			name: "a run with no working directory at all",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.callIn(t, []string{}, arguments(t, "notes.md", "hello\n", false))
			},
			want: []string{"no working directory", "工作区"},
		},
		{
			name: "an empty path",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "   ", "hello\n", false))
			},
			want: []string{"a path is required"},
		},
		{
			name: "no content at all",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, `{"path":"notes.md"}`)
			},
			want: []string{"content is required"},
		},
		{
			name: "content with a NUL byte",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, `{"path":"notes.md","content":"a\u0000b"}`)
			},
			want: []string{"NUL"},
		},
		{
			name: "content over the limit",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", strings.Repeat("x", MaxContentBytes+1), false))
			},
			want: []string{
				fmt.Sprintf("%d bytes", MaxContentBytes+1),
				fmt.Sprintf("%d-byte limit", MaxContentBytes),
			},
		},
		{
			name: "the file being replaced is over the limit",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				f.write(t, "notes.md", strings.Repeat("x", MaxContentBytes+1))
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "small\n", false))
			},
			want: []string{"could not read back in full", fmt.Sprintf("%d-byte limit", MaxContentBytes)},
		},
		{
			name: "the file being replaced is not text",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				f.write(t, "notes.md", "a\x00b")
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "small\n", false))
			},
			want: []string{"not text", "no diff"},
		},
		{
			name: "the file being replaced cannot be read",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				path := f.write(t, "notes.md", "secret\n")
				if err := os.Chmod(path, 0o000); err != nil {
					t.Fatalf("chmod: %v", err)
				}
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "small\n", false))
			},
			want: []string{"read", "notes.md", "permission denied"},
		},
		{
			name: "create_only and the file is there",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				f.write(t, "notes.md", "kept\n")
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "new\n", true))
			},
			want: []string{"create_only", "nothing was written"},
		},
		{
			name: "the target is a symbolic link",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				f.write(t, "real.md", "real\n")
				if err := os.Symlink("real.md", filepath.Join(f.root, "link.md")); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "link.md", "followed\n", false))
			},
			want: []string{"symbolic link"},
		},
		{
			name: "the directory the file would go in is not there",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "missing/notes.md", "hello\n", false))
			},
			want: []string{"file not found"},
		},
		{
			name: "the directory the file would go in cannot be written",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				docs := filepath.Join(f.root, "docs")
				if err := os.Mkdir(docs, 0o555); err != nil {
					t.Fatalf("mkdir docs: %v", err)
				}
				if err := os.Chmod(docs, 0o555); err != nil {
					t.Fatalf("chmod docs: %v", err)
				}
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "docs/notes.md", "hello\n", false))
			},
			want: []string{"docs/notes.md", "permission denied"},
		},
		{
			name: "a settings file that cannot be read",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				f.breakSettings(t)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, arguments(t, "notes.md", "hello\n", false))
			},
			want: []string{"settings.yaml", "is a directory"},
		},
		{
			name: "an argument that is not part of the call",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, `{"path":"notes.md","content":"x","mode":"0600"}`)
			},
			want: []string{"unknown field"},
		},
		{
			name: "two JSON values in one call",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, `{"path":"a.md","content":"x"}{"path":"b.md","content":"y"}`)
			},
			want: []string{"exactly one JSON object"},
		},
		{
			name: "arguments that are not JSON",
			world: func(t *testing.T) *fixture {
				f := newFixture(t)
				f.allow(t, f.root)
				return f
			},
			call: func(t *testing.T, f *fixture) (string, error) {
				return f.call(t, "write notes.md please")
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.world(t)
			before := snapshot(t, f.parent)
			got, err := tc.call(t, f)
			if err == nil {
				t.Fatalf("the call was accepted, result=%q", got)
			}
			if got != "" {
				t.Fatalf("a refused call returned a result: %q", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal=%q, want it to name %q", err, want)
				}
			}
			// An input or a boundary the model can act on is a call-level
			// refusal: the round must not end over it.
			if plugin.IsUnavailable(err) {
				t.Errorf("a refusal of this call is marked as an infrastructure failure: %v", err)
			}
			mustBeUnchanged(t, before, f.parent)
		})
	}
}

// A creation is the result line the model reads to the user, followed by a diff
// of every line the new file has.
func TestANewFileIsCreatedWithItsContentAndADiffOfEveryLine(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	got, err := f.call(t, arguments(t, "notes.md", "one\ntwo\n", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	want := "created notes.md (8 bytes, mode 0644)\ndiff:\n+one\n+two\n"
	if got != want {
		t.Fatalf("result=%q, want %q", got, want)
	}
	if content := f.read(t, "notes.md"); content != "one\ntwo\n" {
		t.Fatalf("notes.md=%q", content)
	}
	if mode := f.modeOf(t, "notes.md"); mode != 0o644 {
		t.Fatalf("mode=%v, want 0644", mode)
	}
}

// An empty string is a legal request: it makes an empty file, and says so.
func TestAnEmptyContentCreatesAnEmptyFile(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	got, err := f.call(t, arguments(t, "empty.md", "", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	want := "created empty.md (0 bytes, mode 0644)\ndiff:\n"
	if got != want {
		t.Fatalf("result=%q, want %q", got, want)
	}
	if content := f.read(t, "empty.md"); content != "" {
		t.Fatalf("empty.md=%q", content)
	}
}

// A name below a directory that is already there is written there, and the
// result names the path the model gave rather than where the file really is.
func TestANewFileIsCreatedInASubdirectory(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	if err := os.Mkdir(filepath.Join(f.root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	got, err := f.call(t, arguments(t, "docs/notes.md", "hello\n", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.HasPrefix(got, "created docs/notes.md (6 bytes, mode 0644)") {
		t.Fatalf("result=%q", got)
	}
	if content := f.read(t, "docs/notes.md"); content != "hello\n" {
		t.Fatalf("docs/notes.md=%q", content)
	}
}

// create_only is only a refusal when the file is there: on a name that is free
// it creates the file like any other call.
func TestCreateOnlyCreatesTheFileWhenItIsNotThere(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	got, err := f.call(t, arguments(t, "fresh.md", "x\n", true))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	want := "created fresh.md (2 bytes, mode 0644)\ndiff:\n+x\n"
	if got != want {
		t.Fatalf("result=%q, want %q", got, want)
	}
	if content := f.read(t, "fresh.md"); content != "x\n" {
		t.Fatalf("fresh.md=%q", content)
	}
}

// An overwrite reports the change: the count of added and removed lines, and a
// diff that leaves the lines the two versions share out of it.
func TestAnOverwriteShowsOnlyTheChangedLines(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	f.write(t, "notes.md", "one\ntwo\nthree\n")
	got, err := f.call(t, arguments(t, "notes.md", "one\nTWO\nthree\n", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	want := "overwrote notes.md (14 bytes, mode 0644): 1 lines added, 1 removed\ndiff:\n-two\n+TWO\n"
	if got != want {
		t.Fatalf("result=%q, want %q", got, want)
	}
	if content := f.read(t, "notes.md"); content != "one\nTWO\nthree\n" {
		t.Fatalf("notes.md=%q", content)
	}
}

// Writing exactly what the file already holds is not a write: the file keeps its
// contents and its modification time.
func TestAnOverwriteWithTheSameContentWritesNothingAtAll(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	path := f.write(t, "notes.md", "same\ntext\n")
	stamp := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	before := snapshot(t, f.parent)

	got, err := f.call(t, arguments(t, "notes.md", "same\ntext\n", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if want := "notes.md already has exactly this content; nothing was written"; got != want {
		t.Fatalf("result=%q, want %q", got, want)
	}
	mustBeUnchanged(t, before, f.parent)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("the file was rewritten: mtime=%s, want %s", info.ModTime(), stamp)
	}
}

// Replacing a file's contents is not the place to also change who may read it.
func TestReplacingAFileKeepsItsPermissionBits(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	path := f.write(t, "secret.md", "old\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	got, err := f.call(t, arguments(t, "secret.md", "new\n", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(got, "mode 0640") {
		t.Fatalf("result=%q, want it to state mode 0640", got)
	}
	if mode := f.modeOf(t, "secret.md"); mode != 0o640 {
		t.Fatalf("mode=%v, want 0640", mode)
	}
	if content := f.read(t, "secret.md"); content != "new\n" {
		t.Fatalf("secret.md=%q", content)
	}
}

// A diff that does not fit is cut, and the result says which limit it hit and
// how much of the diff it could not give. The counts above it are the counts of
// the whole change: they say what happened to the file, and the note says how
// much of the evidence is missing.
func TestALargeDiffIsCutAndSaysWhichLimitItHit(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	const lines = 400
	var old, updated strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&old, "old line %03d %s\n", i, strings.Repeat(".", 40))
		fmt.Fprintf(&updated, "new line %03d %s\n", i, strings.Repeat(".", 40))
	}
	f.write(t, "big.md", old.String())

	got, err := f.call(t, arguments(t, "big.md", updated.String(), false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	header, diff, ok := strings.Cut(got, "\ndiff:\n")
	if !ok {
		t.Fatalf("result=%q, want a diff section", got)
	}
	if want := fmt.Sprintf("overwrote big.md (%d bytes, mode 0644): %d lines added, %d removed", len(updated.String()), lines, lines); header != want {
		t.Fatalf("header=%q, want %q", header, want)
	}

	// The whole change is far larger than the limit, and the note has to account
	// for every byte of it that is not in the result.
	full, added, removed := diffText(old.String(), updated.String())
	if added != lines || removed != lines {
		t.Fatalf("the fixture's diff counts %d added and %d removed, want %d and %d", added, removed, lines, lines)
	}
	if len(full) <= MaxDiffBytes {
		t.Fatalf("the fixture's diff is %d bytes, over the %d-byte limit is what this test needs", len(full), MaxDiffBytes)
	}
	body, note, ok := strings.Cut(diff, "\n… ")
	if !ok {
		t.Fatalf("diff=%q, want it to say that it was cut", diff)
	}
	body += "\n"
	if len(body) == 0 || len(body) > MaxDiffBytes {
		t.Fatalf("the diff section is %d bytes, want one that is shown and within the %d-byte limit", len(body), MaxDiffBytes)
	}
	shown := regexp.MustCompile(`(\d+) more bytes of it are not shown`).FindStringSubmatch(note)
	if shown == nil {
		t.Fatalf("note=%q, want it to say how much of the diff is missing", note)
	}
	if want := fmt.Sprintf("the diff was cut at the %d-byte limit", MaxDiffBytes); !strings.Contains(note, want) {
		t.Fatalf("note=%q, want it to name the limit it hit (%q)", note, want)
	}
	missing, convErr := strconv.Atoi(shown[1])
	if convErr != nil {
		t.Fatalf("note=%q: %v", note, convErr)
	}
	if want := len(full) - len(body); missing != want {
		t.Fatalf("the note says %d bytes are missing, want %d (the diff is %d bytes, %d were shown)", missing, want, len(full), len(body))
	}
	// What is shown is only ever the two sides of the change, in that order.
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if !strings.HasPrefix(line, "-old line ") && !strings.HasPrefix(line, "+new line ") {
			t.Fatalf("a line of the diff is not a changed line: %q", line)
		}
	}
	if !strings.HasPrefix(body, "-old line 000") {
		t.Fatalf("the diff does not start at the first changed line: %q", body)
	}
}

// The result and the refusals name the path the model gave, never the host path
// the file really lives at: the model has no business learning the layout of the
// machine it runs on.
func TestTheResultNamesThePathTheModelGave(t *testing.T) {
	f := newFixture(t)
	f.allow(t, f.root)
	got, err := f.call(t, arguments(t, "notes.md", "hello\n", false))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if strings.Contains(got, f.root) {
		t.Fatalf("the result carries a host path: %q", got)
	}
	if !strings.HasPrefix(got, "created notes.md (") {
		t.Fatalf("result=%q, want the relative path the model gave", got)
	}
	if _, err := f.call(t, arguments(t, "missing/notes.md", "hello\n", false)); !strings.Contains(err.Error(), "missing/notes.md") || strings.Contains(err.Error(), f.root) {
		t.Fatalf("refusal=%v, want the relative path and no host path", err)
	}
}

// The settings file is read on every call rather than once at start-up, so a
// directory the user allows while this conversation is going is allowed for the
// model's next call — no restart and no rebuilt tool.
func TestAGrantMadeAfterTheFirstCallIsSeenByTheNextOne(t *testing.T) {
	f := newFixture(t)
	if _, err := f.call(t, arguments(t, "notes.md", "hello\n", false)); err == nil {
		t.Fatal("the call was accepted before any directory was allowed")
	}
	f.allow(t, f.root)
	if _, err := f.call(t, arguments(t, "notes.md", "hello\n", false)); err != nil {
		t.Fatalf("Invoke after the grant: %v", err)
	}
	if content := f.read(t, "notes.md"); content != "hello\n" {
		t.Fatalf("notes.md=%q", content)
	}
}

// The schema is the contract the model reads: three parameters, two of them
// required, and nothing else accepted.
func TestTheSchemaIsStrictAndRequiresPathAndContent(t *testing.T) {
	encoded, err := json.Marshal(NewWriteTool("").Schema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["type"] != "object" || raw["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %s", encoded)
	}
	required, ok := raw["required"].([]any)
	if !ok || len(required) != 2 || required[0] != "path" || required[1] != "content" {
		t.Fatalf("required=%v, want [path content]", raw["required"])
	}
	properties, ok := raw["properties"].(map[string]any)
	if !ok || len(properties) != 3 {
		t.Fatalf("properties=%v, want path, content and create_only", raw["properties"])
	}
	for _, name := range []string{"path", "content", "create_only"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("the schema does not expose %q: %s", name, encoded)
		}
	}
	createOnly, _ := properties["create_only"].(map[string]any)
	if createOnly["type"] != "boolean" {
		t.Fatalf("create_only=%v, want a boolean", createOnly)
	}
}

// The copy is model-facing: it says what the call does, what it will not do, and
// that a refusal leaves everything as it was.
func TestTheCopyTellsTheModelWhatACallDoes(t *testing.T) {
	tool := NewWriteTool("")
	if tool.Name() != WriteToolName {
		t.Fatalf("tool name=%q, want %q", tool.Name(), WriteToolName)
	}
	text := tool.Description()
	for _, want := range []string{
		"create_only",
		"diff",
		"replaces what the file holds",
		"relative to one of this run's working directories",
		"refused",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the copy must say %q: %q", want, text)
		}
	}
}

// A tool that cannot be given a settings file at all writes nothing: an empty
// path is the same answer as a file that is not there, and the refusal still
// tells the user where to allow a directory.
func TestASettingsFileThatIsNotConfiguredRefusesAndSaysWhere(t *testing.T) {
	f := newFixture(t)
	f.tool = NewWriteTool("")
	_, err := f.call(t, arguments(t, "notes.md", "hello\n", false))
	if err == nil {
		t.Fatal("the call was accepted with no settings file configured")
	}
	if !strings.Contains(err.Error(), "no directory has been allowed") || !strings.Contains(err.Error(), "setting") {
		t.Fatalf("refusal=%q, want it to point at the settings page", err)
	}
}
