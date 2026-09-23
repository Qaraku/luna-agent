package uiplugin

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/fileread"
)

// manifestText builds a valid manifest for name.
func manifestText(name string) string {
	return `{"name":"` + name + `","title":"` + name + `","description":"fixture","entry":"plugin.js"}`
}

// writePlugin creates <root>/<name>/ holding the given manifest and files. An
// empty manifest leaves plugin.json out entirely, which is the "missing
// manifest" case.
func writePlugin(t *testing.T, root, name, manifest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create plugin directory: %v", err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", name, manifestFile, err)
		}
	}
	for file, content := range files {
		path := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s/%s parent: %v", name, file, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", name, file, err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func names(manifests []Manifest) []string {
	out := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		out = append(out, manifest.Name)
	}
	return out
}

func reasons(skipped []Skipped) map[string]string {
	out := map[string]string{}
	for _, entry := range skipped {
		out[entry.Name] = entry.Reason
	}
	return out
}

// A listing is name-ordered, and one unusable directory is a reported skip
// rather than a failure or a silent hole.
func TestListOrdersByPluginNameAndReportsEverySkip(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "beta", manifestText("beta"), map[string]string{"plugin.js": "export function mount() {}\n"})
	writePlugin(t, root, "alpha", manifestText("alpha"), map[string]string{"plugin.js": "export function mount() {}\n"})
	writePlugin(t, root, "malformed", "{ this is not json", nil)
	writePlugin(t, root, "bare", "", nil)
	writePlugin(t, root, "misnamed", manifestText("other"), nil)
	writePlugin(t, root, "Bad_Name", manifestText("Bad_Name"), nil)
	writePlugin(t, root, "incomplete", `{"name":"incomplete","description":"no title or entry"}`, nil)
	writePlugin(t, root, "escaping", `{"name":"escaping","title":"escaping","entry":"../beta/plugin.js"}`, nil)
	// A manifest that is a link out of the plugin directory is not read.
	writePlugin(t, root, "linked", "", nil)
	writeFile(t, filepath.Join(root, "elsewhere.json"), manifestText("linked"))
	if err := os.Symlink(filepath.Join(root, "elsewhere.json"), filepath.Join(root, "linked", manifestFile)); err != nil {
		t.Fatalf("create manifest link: %v", err)
	}
	// A file at the root is not a plugin candidate, so it is not a skip either.
	writeFile(t, filepath.Join(root, "README.md"), "notes, not a plugin\n")

	manifests, skipped, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := names(manifests); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("plugins = %v, want [alpha beta] in name order", got)
	}
	got := reasons(skipped)
	want := map[string]string{
		"malformed":  ReasonManifestMalformed,
		"bare":       ReasonManifestMissing,
		"linked":     ReasonManifestUnreadable,
		"misnamed":   ReasonNameMismatch,
		"Bad_Name":   ReasonNameInvalid,
		"incomplete": ReasonFieldMissing,
		"escaping":   ReasonEntryInvalid,
	}
	if len(got) != len(want) {
		t.Fatalf("skipped = %+v, want %d entries", skipped, len(want))
	}
	for name, reason := range want {
		if got[name] != reason {
			t.Fatalf("skip reason for %q = %q, want %q", name, got[name], reason)
		}
	}
	for _, entry := range manifests {
		if entry.Title == "" || entry.Entry != "plugin.js" || entry.Description != "fixture" {
			t.Fatalf("listed manifest is incomplete: %+v", entry)
		}
	}
}

// A root that is not configured or not present holds no plugins; that is a
// state and not a failure, so the endpoint behind List cannot turn it into a
// server error.
func TestListOfUnconfiguredOrAbsentRootIsEmpty(t *testing.T) {
	for _, root := range []string{"", "   ", filepath.Join(t.TempDir(), "absent")} {
		manifests, skipped, err := List(root)
		if err != nil {
			t.Fatalf("List(%q): %v", root, err)
		}
		if len(manifests) != 0 || len(skipped) != 0 {
			t.Fatalf("List(%q) = %d plugins, %d skips; want both empty", root, len(manifests), len(skipped))
		}
	}
}

// A root that exists but cannot be read is an error rather than an empty list,
// and that error must not carry the host path: it becomes a browser-visible
// message.
func TestListErrorNamesNoHostPathWhenTheRootCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 directory")
	}
	root := t.TempDir()
	writePlugin(t, root, "demo", manifestText("demo"), nil)
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod the plugin root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if _, _, err := List(root); err == nil {
		t.Fatal("List accepted a root it cannot read")
	} else if strings.Contains(err.Error(), root) {
		t.Fatalf("error %q leaks the host path of the plugin root", err)
	}
}

func TestValidNameAcceptsOnlyDirectoryNames(t *testing.T) {
	for _, name := range []string{"a", "demo", "counter", "read-file", "0", strings.Repeat("a", 32)} {
		if !ValidName(name) {
			t.Fatalf("ValidName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../demo", "demo/../other", "a/b", "/demo", "Demo", "a_b", "a.b", "a b", "演示", strings.Repeat("a", 33)} {
		if ValidName(name) {
			t.Fatalf("ValidName(%q) = true, want false", name)
		}
	}
}

// A file request can only name a file inside the plugin directory: after
// normalization and after resolving symbolic links the path must still be
// there. Every refusal returns no content.
func TestFileRefusesAnythingOutsideThePluginDirectory(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "demo", manifestText("demo"), map[string]string{"plugin.js": "export function mount() {}\n"})
	// A file inside the UI plugin root but outside the plugin directory, a link
	// from inside the plugin directory to it, and a linked directory.
	writeFile(t, filepath.Join(root, "secret.js"), "SHOULD NEVER BE SERVED\n")
	if err := os.Symlink(filepath.Join(root, "secret.js"), filepath.Join(root, "demo", "link.js")); err != nil {
		t.Fatalf("create file link: %v", err)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "plugin.js"), "SHOULD NEVER BE SERVED\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create directory link: %v", err)
	}

	cases := []struct {
		name string
		dir  string
		file string
		want error
	}{
		{"dotdot escape", "demo", "../secret.js", fileread.ErrPathEscape},
		{"dotdot escape above the root", "demo", "../../secret.js", fileread.ErrPathEscape},
		{"dotdot in a nested path", "demo", "nested/../../secret.js", fileread.ErrPathEscape},
		{"symlink out of the plugin directory", "demo", "link.js", fileread.ErrSymlinkEscape},
		{"symlinked plugin directory", "linked", "plugin.js", fileread.ErrNotFound},
		{"absolute path", "demo", "/etc/hosts.js", fileread.ErrPathAbsolute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content, contentType, err := File(root, tc.dir, tc.file)
			if !errors.Is(err, tc.want) {
				t.Fatalf("File(%q, %q) error = %v, want %v", tc.dir, tc.file, err, tc.want)
			}
			if content != "" || contentType != "" {
				t.Fatalf("refused request returned content %q of type %q", content, contentType)
			}
			if strings.Contains(err.Error(), root) {
				t.Fatalf("error %q leaks the host path of the plugin root", err)
			}
		})
	}
}

// The extension is resolved before the filesystem is touched, and an unknown
// extension is refused rather than guessed or served as bytes.
func TestFileRefusesExtensionsItDoesNotServe(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "demo", manifestText("demo"), map[string]string{
		"plugin.js":   "export function mount() {}\n",
		"notes.txt":   "plain text\n",
		"data.yaml":   "a: b\n",
		"noextension": "x\n",
		"page.HTML":   "<p>html</p>\n",
	})
	for _, file := range []string{"notes.txt", "data.yaml", "noextension", "page.HTML", "absent.txt"} {
		content, contentType, err := File(root, "demo", file)
		if !errors.Is(err, ErrFileType) {
			t.Fatalf("File(demo, %q) error = %v, want %v", file, err, ErrFileType)
		}
		if content != "" || contentType != "" {
			t.Fatalf("refused request for %q returned content %q of type %q", file, content, contentType)
		}
	}
}

func TestFileRefusesNamesThatAreNotPluginNames(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "demo", manifestText("demo"), map[string]string{"plugin.js": "export function mount() {}\n"})
	for _, name := range []string{"", ".", "..", "../demo", "demo/../other", "/demo", "Demo", "a_b"} {
		if _, _, err := File(root, name, "plugin.js"); !errors.Is(err, ErrNameInvalid) {
			t.Fatalf("File(%q, plugin.js) error = %v, want %v", name, err, ErrNameInvalid)
		}
	}
	// The control: the same request with a valid name is served, so the
	// refusals above are the name check and not something else.
	if _, _, err := File(root, "demo", "plugin.js"); err != nil {
		t.Fatalf("File(demo, plugin.js): %v", err)
	}
}

func TestFileMissingCasesAreNotFound(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "demo", manifestText("demo"), map[string]string{"plugin.js": "export function mount() {}\n"})
	if err := os.MkdirAll(filepath.Join(root, "demo", "subdir.js"), 0o755); err != nil {
		t.Fatalf("create directory named like a file: %v", err)
	}
	cases := []struct {
		name string
		dir  string
		file string
		want error
	}{
		{"missing file", "demo", "absent.js", fileread.ErrNotFound},
		{"missing plugin", "nosuchplugin", "plugin.js", fileread.ErrNotFound},
		{"plugin is a directory with no manifest", "demo", "nested/absent.js", fileread.ErrNotFound},
		{"directory where a file is expected", "demo", "subdir.js", fileread.ErrNotRegular},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := File(root, tc.dir, tc.file); !errors.Is(err, tc.want) {
				t.Fatalf("File(%q, %q) error = %v, want %v", tc.dir, tc.file, err, tc.want)
			}
		})
	}
}

func TestFileServesKnownTypesWithFixedContentTypes(t *testing.T) {
	root := t.TempDir()
	source := "export function mount(target, api) {}\nexport function unmount(target) {}\n"
	writePlugin(t, root, "demo", manifestText("demo"), map[string]string{
		"plugin.js":       source,
		"plugin.json":     manifestText("demo"),
		"nested/asset.js": "export const asset = 1;\n",
		"UPPER.JSON":      "{}\n",
	})
	cases := []struct {
		file        string
		contentType string
		content     string
	}{
		{"plugin.js", "text/javascript", source},
		{"plugin.json", "application/json", manifestText("demo")},
		{"nested/asset.js", "text/javascript", "export const asset = 1;\n"},
		{"UPPER.JSON", "application/json", "{}\n"},
	}
	for _, tc := range cases {
		content, contentType, err := File(root, "demo", tc.file)
		if err != nil {
			t.Fatalf("File(demo, %q): %v", tc.file, err)
		}
		if contentType != tc.contentType || content != tc.content {
			t.Fatalf("File(demo, %q) = (%q, %q), want (%q, %q)", tc.file, content, contentType, tc.content, tc.contentType)
		}
	}
}

// An oversize file is refused instead of truncated, like the file-reading tool.
func TestFileRefusesContentOverTheCap(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", fileread.DefaultLimit+1)
	writePlugin(t, root, "demo", manifestText("demo"), map[string]string{"big.js": big})
	if _, _, err := File(root, "demo", "big.js"); !errors.Is(err, fileread.ErrTooLarge) {
		t.Fatalf("File(demo, big.js) error = %v, want %v", err, fileread.ErrTooLarge)
	}
}

// The two example plugins the checkout ships are the contract made concrete:
// they are discovered in name order and their entry files are servable modules
// that export both halves of the mount/unmount contract.
func TestExamplePluginsAreDiscoveredAndServable(t *testing.T) {
	root := filepath.Join("..", "..", "plugins", Dir)
	manifests, skipped, err := List(root)
	if err != nil {
		t.Fatalf("List(%s): %v", root, err)
	}
	if len(skipped) != 0 {
		t.Fatalf("the checkout ships a plugin that cannot be listed: %+v", skipped)
	}
	if got := names(manifests); !slices.Equal(got, []string{"counter", "hello"}) {
		t.Fatalf("example plugins = %v, want [counter hello]", got)
	}
	for _, manifest := range manifests {
		if manifest.Title == "" || manifest.Description == "" {
			t.Fatalf("%s: manifest is missing its title or description: %+v", manifest.Name, manifest)
		}
		if !entryPath(manifest.Entry) {
			t.Fatalf("%s: entry %q is not a path inside the plugin", manifest.Name, manifest.Entry)
		}
		content, contentType, err := File(root, manifest.Name, manifest.Entry)
		if err != nil {
			t.Fatalf("%s: entry %q is not servable: %v", manifest.Name, manifest.Entry, err)
		}
		if contentType != "text/javascript" {
			t.Fatalf("%s: entry content type = %q, want text/javascript", manifest.Name, contentType)
		}
		for _, exported := range []string{"export function mount", "export function unmount"} {
			if !strings.Contains(content, exported) {
				t.Fatalf("%s: entry does not export the contract: missing %q", manifest.Name, exported)
			}
		}
		if strings.Contains(content, "innerHTML") || strings.Contains(content, "eval(") {
			t.Fatalf("%s: entry uses innerHTML or eval", manifest.Name)
		}
	}
}
