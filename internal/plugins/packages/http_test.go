package packages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/httpapi"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/runconfig"
	"github.com/Qaraku/luna-agent/internal/store"
)

type packageHTTPHost struct{}

func (packageHTTPHost) State() pluginhost.State              { return pluginhost.State{} }
func (packageHTTPHost) Reload(context.Context, string) error { return nil }

type packagePreferenceSpy struct{ calls int }

func (p *packagePreferenceSpy) SetEnabled(string, bool) error { p.calls++; return nil }
func TestPackageMutationOriginsAndManagedStatePersistence(t *testing.T) {
	r := packageRegistry()
	m, _ := OpenManager(t.TempDir(), t.TempDir(), r)
	if err := r.Register(m); err != nil {
		t.Fatal(err)
	}
	r.Enable(ManagerID)
	sessions, _ := store.Open(t.TempDir())
	pref := &packagePreferenceSpy{}
	h := httpapi.New(packageHTTPHost{}, nil, sessions, httpapi.Info{BoundHost: "127.0.0.1:43210"}, httpapi.WithCapabilities(r), httpapi.WithCapabilityPreference(pref))
	request := func(path, body string, origin bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210"+path, strings.NewReader(body))
		req.Host = "127.0.0.1:43210"
		if origin {
			req.Header.Set("Origin", "http://127.0.0.1:43210")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/api/packages/install", "/api/packages/activate", "/api/packages/disable", "/api/packages/remove"} {
		if w := request(path, "{}", false); w.Code != 403 {
			t.Fatalf("unguarded %s=%d", path, w.Code)
		}
	}
	source := t.TempDir()
	manifest := Manifest{Format: 1, ID: "data", Version: "1", Title: "Data", Files: []string{}, Presets: []runconfig.Selection{{ID: "simple", Title: "Simple"}}}
	raw, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(source, ManifestName), raw, 0600)
	v, err := m.Install(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "data", Revision: v.Revision, Expected: v.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/plugins/pkg-data/disable", "{}", true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if pref.calls != 0 || m.List()[0].Enabled {
		t.Fatal("generic toggle wrote a second preference source")
	}
	if err = m.Remove("data", v.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Entry("pkg-data"); ok {
		t.Fatal("removed entry remains")
	}
}
func TestUIPackageNeedsSeparateTrustAndURLsTrackRevision(t *testing.T) {
	r := packageRegistry()
	m, _ := OpenManager(t.TempDir(), t.TempDir(), r)
	source := t.TempDir()
	manifest := Manifest{Format: 1, ID: "display", Version: "1", Title: "Display", Files: []string{"panel.js"}, Static: []string{"panel.js"}, Panels: []Surface{{ID: "panel", Title: "Display", Entry: "panel.js"}}}
	raw, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(source, ManifestName), raw, 0600)
	os.WriteFile(filepath.Join(source, "panel.js"), []byte("export function mount(){}; export function unmount(){}"), 0600)
	a, err := m.Install(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "display", Revision: a.Revision, Expected: a.Revision, Confirm: true}); err == nil {
		t.Fatal("UI code enabled without trust")
	}
	if err = m.Activate(context.Background(), Activation{ID: "display", Revision: a.Revision, Expected: a.Revision, Confirm: true, TrustUI: true}); err != nil {
		t.Fatal(err)
	}
	old, _ := r.Entry("pkg-display")
	oldURL := old.Plugin.(plugin.PanelProvider).Panels()[0].Entry
	os.WriteFile(filepath.Join(source, "panel.js"), []byte("export function mount(){return 2}; export function unmount(){}"), 0600)
	b, err := m.Install(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "display", Revision: b.Revision, Expected: a.Revision, Confirm: true, TrustUI: true}); err != nil {
		t.Fatal(err)
	}
	next, _ := r.Entry("pkg-display")
	if next.Plugin.(plugin.PanelProvider).Panels()[0].Entry == oldURL {
		t.Fatal("UI upgrade reused a cached module URL")
	}
}
