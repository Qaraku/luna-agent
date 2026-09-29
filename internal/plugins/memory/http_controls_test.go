package memory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/httpapi"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
)

type memoryHTTPHost struct{}

func (memoryHTTPHost) State() pluginhost.State              { return pluginhost.State{} }
func (memoryHTTPHost) Reload(context.Context, string) error { return nil }
func TestMemoryUserMutationsRequireExactHostOrigin(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := plugin.NewRegistry(plugin.PermissionStateWrite)
	if err = registry.Register(p); err != nil {
		t.Fatal(err)
	}
	if err = registry.Enable(PluginID); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(memoryHTTPHost{}, nil, sessions, httpapi.Info{BoundHost: "127.0.0.1:43210"}, httpapi.WithCapabilities(registry))
	for _, path := range []string{AddRoutePath, CorrectRoutePath, RestoreRoutePath, RetractRoutePath} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210"+path, strings.NewReader("{}"))
		req.Host = "127.0.0.1:43210"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatalf("unguarded %s=%d", path, w.Code)
		}
	}
}
