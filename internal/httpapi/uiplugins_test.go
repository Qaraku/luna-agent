package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

const pluginSource = "export function mount(target, api) {}\nexport function unmount(target) {}\n"

// uiRoot builds a UI plugin root shaped like plugins/ui: two healthy plugins,
// files that must never be served, and directories whose manifests are broken
// in the ways the listing has to survive.
func uiRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	manifest := func(name string) string {
		return `{"name":"` + name + `","title":"` + name + `","description":"fixture","entry":"plugin.js"}`
	}
	write("demo/plugin.json", manifest("demo"))
	write("demo/plugin.js", pluginSource)
	write("demo/notes.txt", "not a served type\n")
	write("demo/subdir.js/placeholder.js", "a directory named like a file\n")
	write("alpha/plugin.json", manifest("alpha"))
	write("alpha/plugin.js", pluginSource)
	write("malformed/plugin.json", "{ not json")
	write("bare/keep", "no plugin.json here\n")
	write("mismatched/plugin.json", manifest("other"))
	// A file inside the plugin root but outside any plugin directory.
	write("secret.js", "SHOULD NEVER BE SERVED\n")
	if err := os.Symlink(filepath.Join(root, "secret.js"), filepath.Join(root, "demo", "link.js")); err != nil {
		t.Fatalf("create link: %v", err)
	}
	return root
}

func uiHandler(t *testing.T, dir string) http.Handler {
	t.Helper()
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web", UIPluginsDir: dir})
}

// decodeUIList decodes the frozen list shape of GET /api/ui-plugins.
func decodeUIList(t *testing.T, body []byte) ([]uiPluginRef, []uiPluginSkip) {
	t.Helper()
	var envelope struct {
		Plugins []uiPluginRef  `json:"plugins"`
		Skipped []uiPluginSkip `json:"skipped"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return envelope.Plugins, envelope.Skipped
}

func TestUIPluginListIsSortedAndReportsSkippedPlugins(t *testing.T) {
	root := uiRoot(t)
	w := request(t, uiHandler(t, root), http.MethodGet, "/api/ui-plugins", "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("CORS header added: %q", got)
	}
	plugins, skipped := decodeUIList(t, w.Body.Bytes())
	var names []string
	for _, plugin := range plugins {
		names = append(names, plugin.Name)
		if plugin.Title == "" || plugin.Entry == "" || plugin.Description == "" {
			t.Fatalf("listed plugin is incomplete: %+v", plugin)
		}
	}
	if strings.Join(names, ",") != "alpha,demo" {
		t.Fatalf("plugins=%v, want [alpha demo] in name order", names)
	}
	got := map[string]string{}
	for _, skip := range skipped {
		got[skip.Name] = skip.Reason
	}
	for _, name := range []string{"malformed", "bare", "mismatched"} {
		if got[name] == "" {
			t.Fatalf("skipped=%+v does not report %q", skipped, name)
		}
	}
	if _, ok := got["secret.js"]; ok || len(got) != 3 {
		t.Fatalf("skipped=%+v, want exactly the three broken plugin directories", skipped)
	}
}

// A root holding only broken plugins is still a 200 with an empty plugin list
// and a visible explanation: one broken plugin never fails the request.
func TestUIPluginListNeverFailsOnBrokenPlugins(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bare", "plugin.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := request(t, uiHandler(t, root), http.MethodGet, "/api/ui-plugins", "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, `"error"`) {
		t.Fatalf("broken plugins produced an error response: %s", body)
	}
	plugins, skipped := decodeUIList(t, w.Body.Bytes())
	if len(plugins) != 0 || len(skipped) != 1 || skipped[0].Name != "bare" {
		t.Fatalf("plugins=%+v skipped=%+v", plugins, skipped)
	}
}

// An unset UI plugin directory is a host state, not a broken plugin: it lists
// nothing and is not a server error either.
func TestUIPluginListOfUnsetRootIsEmpty(t *testing.T) {
	w := request(t, uiHandler(t, ""), http.MethodGet, "/api/ui-plugins", "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	plugins, skipped := decodeUIList(t, w.Body.Bytes())
	if len(plugins) != 0 || len(skipped) != 0 {
		t.Fatalf("plugins=%+v skipped=%+v, want both empty", plugins, skipped)
	}
}

func TestUIPluginFileIsServedWithItsTypeAndNoCORS(t *testing.T) {
	root := uiRoot(t)
	w := request(t, uiHandler(t, root), http.MethodGet, "/api/ui-plugins/demo/plugin.js", "", false)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/javascript" {
		t.Fatalf("content type=%q, want text/javascript", got)
	}
	if w.Body.String() != pluginSource {
		t.Fatalf("body=%q, want the plugin source", w.Body.String())
	}
	for header := range w.Header() {
		if strings.HasPrefix(header, "Access-Control-") {
			t.Fatalf("CORS header %q added to a plugin file response", header)
		}
	}
	if w := request(t, uiHandler(t, root), http.MethodGet, "/api/ui-plugins/demo/plugin.json", "", false); w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("manifest content type=%q, want application/json", w.Header().Get("Content-Type"))
	}
}

func TestUIPluginFileRefusals(t *testing.T) {
	root := uiRoot(t)
	cases := []struct {
		name   string
		path   string
		status int
	}{
		{"dotdot escape", "/api/ui-plugins/demo/../secret.js", 400},
		{"encoded dotdot escape", "/api/ui-plugins/demo/%2e%2e%2fsecret.js", 400},
		{"symlink out of the plugin directory", "/api/ui-plugins/demo/link.js", 400},
		{"absolute path", "/api/ui-plugins/demo/%2fetc%2fhosts.js", 400},
		{"name is not a plugin name", "/api/ui-plugins/Demo/plugin.js", 400},
		// An encoded separator is decoded by net/url before routing, so it
		// becomes a path inside the named plugin rather than a second plugin
		// name. It stays contained, and this path does not exist.
		{"encoded separator", "/api/ui-plugins/demo%2fother/plugin.js", 404},
		{"unknown extension", "/api/ui-plugins/demo/notes.txt", 415},
		{"directory instead of a file", "/api/ui-plugins/demo/subdir.js", 404},
		{"missing file", "/api/ui-plugins/demo/absent.js", 404},
		{"missing plugin", "/api/ui-plugins/nosuchplugin/plugin.js", 404},
		{"no file named", "/api/ui-plugins/demo/", 404},
		{"no plugin named", "/api/ui-plugins/", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, uiHandler(t, root), http.MethodGet, tc.path, "", false)
			if w.Code != tc.status {
				t.Fatalf("GET %s status=%d, want %d; body=%s", tc.path, w.Code, tc.status, w.Body.String())
			}
			if strings.Contains(w.Body.String(), root) {
				t.Fatalf("response leaks the host path of the plugin root: %s", w.Body.String())
			}
			for header := range w.Header() {
				if strings.HasPrefix(header, "Access-Control-") {
					t.Fatalf("CORS header %q added to a refusal", header)
				}
			}
			if strings.Contains(w.Body.String(), "SHOULD NEVER BE SERVED") {
				t.Fatalf("refusal served content from outside the plugin directory: %s", w.Body.String())
			}
		})
	}
}

func TestUIPluginEndpointsOnlyAnswerGET(t *testing.T) {
	root := uiRoot(t)
	h := uiHandler(t, root)
	for _, path := range []string{"/api/ui-plugins", "/api/ui-plugins/demo/plugin.js"} {
		w := request(t, h, http.MethodPost, path, `{}`, true)
		if w.Code != 405 || w.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("POST %s status=%d allow=%q", path, w.Code, w.Header().Get("Allow"))
		}
	}
}
