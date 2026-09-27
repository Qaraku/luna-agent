package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/plugins/memory"
)

// fakeCapability is a minimal contributed capability for the HTTP layer: it
// serves the routes it declares and counts the calls it received. Nothing here
// knows what the routes mean — that is the point of the layer under test.
type fakeCapability struct {
	id     string
	routes []pluginRoute
	hits   int
}

type pluginRoute struct {
	method, path string
}

func (r pluginRoute) Method() string { return r.method }
func (r pluginRoute) Path() string   { return r.path }
func (r pluginRoute) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	send(w, 200, map[string]any{"served": r.path})
}

func (c *fakeCapability) Descriptor() plugin.Descriptor {
	d := plugin.Descriptor{ID: c.id, Title: "Test " + c.id, Deployment: plugin.DeploymentBuiltin}
	for _, r := range c.routes {
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionRoute, ID: r.path})
	}
	d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimRoutePrefix, ID: "/api/" + c.id})
	return d
}

func (c *fakeCapability) Routes() []plugin.Route {
	routes := make([]plugin.Route, 0, len(c.routes))
	for _, r := range c.routes {
		routes = append(routes, countingRoute{r, c})
	}
	return routes
}

// countingRoute records that the kernel actually dispatched to the capability.
type countingRoute struct {
	inner pluginRoute
	owner *fakeCapability
}

func (r countingRoute) Method() string { return r.inner.method }
func (r countingRoute) Path() string   { return r.inner.path }
func (r countingRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.owner.hits++
	r.inner.ServeHTTP(w, req)
}

// handlerWithCapability wires a registry holding one enabled capability.
func handlerWithCapability(t *testing.T, c *fakeCapability) http.Handler {
	t.Helper()
	reg := plugin.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Enable(c.id); err != nil {
		t.Fatalf("enable: %v", err)
	}
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithCapabilities(reg))
}

func capabilityRequest(t *testing.T, h http.Handler, method, path string, withOrigin bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = "127.0.0.1:43210"
	if withOrigin {
		req.Header.Set("Origin", "http://127.0.0.1:43210")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAContributedRouteServesItsOwnPathAndMethod(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	w := capabilityRequest(t, handlerWithCapability(t, c), http.MethodGet, "/api/notes", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/api/notes") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if c.hits != 1 {
		t.Fatalf("the capability was not dispatched to: hits=%d", c.hits)
	}
}

// A read route reached with another method is a 405, and — like a Kernel route
// in the same position — it needs no Origin, because the request is not a
// mutation of this capability's making.
func TestAContributedReadRouteRejectsAnotherMethod(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	w := capabilityRequest(t, handlerWithCapability(t, c), http.MethodPost, "/api/notes", false)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405 (body=%s)", w.Code, w.Body.String())
	}
	if c.hits != 0 {
		t.Fatal("a wrong-method request must not reach the capability")
	}
}

func TestAContributedMutationRequiresTheBoundOrigin(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodPost, path: "/api/notes/do"}}}
	h := handlerWithCapability(t, c)

	if w := capabilityRequest(t, h, http.MethodPost, "/api/notes/do", false); w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 without an Origin (body=%s)", w.Code, w.Body.String())
	}
	if w := capabilityRequest(t, h, http.MethodPost, "/api/notes/do", true); w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 with the bound Origin (body=%s)", w.Code, w.Body.String())
	}
	if c.hits != 1 {
		t.Fatalf("hits=%d, want exactly the request that carried the Origin", c.hits)
	}
}

func TestAnUnknownPathIsStillNotFound(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	w := capabilityRequest(t, handlerWithCapability(t, c), http.MethodGet, "/api/elsewhere", false)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

// The kernel reports what a capability declares and whether it is in service;
// it has none of the capability's own data to report, and must not invent any.
func TestStateListsCapabilitiesWithoutTheirData(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	w := capabilityRequest(t, handlerWithCapability(t, c), http.MethodGet, "/api/state", false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var state struct {
		Capabilities []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			Deployment    string `json:"deployment"`
			State         string `json:"state"`
			Contributions []struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"contributions"`
			Claims []struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"claims"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v (body=%s)", err, w.Body.String())
	}
	if len(state.Capabilities) != 1 {
		t.Fatalf("capabilities=%+v", state.Capabilities)
	}
	got := state.Capabilities[0]
	if got.ID != "notes" || got.Deployment != "builtin" || got.State != "enabled" {
		t.Fatalf("capability=%+v", got)
	}
	if len(got.Contributions) != 1 || got.Contributions[0].Kind != "route" || got.Contributions[0].ID != "/api/notes" {
		t.Fatalf("contributions=%+v", got.Contributions)
	}
	if len(got.Claims) != 1 || got.Claims[0].Kind != "route-prefix" {
		t.Fatalf("claims=%+v", got.Claims)
	}
	if strings.Contains(w.Body.String(), "served") {
		t.Fatalf("state leaked capability data: %s", w.Body.String())
	}
}

// Disabling a capability takes its routes out of service with the rest of it,
// and enabling it again brings them back. Nothing about the capability's data
// changes: the kernel never touches it.
func TestDisableTakesTheCapabilityOutOfServiceAndEnableBringsItBack(t *testing.T) {
	c := &fakeCapability{id: "notes", routes: []pluginRoute{{method: http.MethodGet, path: "/api/notes"}}}
	h := handlerWithCapability(t, c)
	if w := capabilityRequest(t, h, http.MethodGet, "/api/notes", false); w.Code != http.StatusOK {
		t.Fatalf("status=%d before disabling", w.Code)
	}

	w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/disable", true)
	if w.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"state":"disabled"`) {
		t.Fatalf("disable body=%s", w.Body.String())
	}
	if w := capabilityRequest(t, h, http.MethodGet, "/api/notes", false); w.Code != http.StatusNotFound {
		t.Fatalf("a disabled capability still served its route: %d", w.Code)
	}

	w = capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/enable", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"enabled"`) {
		t.Fatalf("enable status=%d body=%s", w.Code, w.Body.String())
	}
	if w := capabilityRequest(t, h, http.MethodGet, "/api/notes", false); w.Code != http.StatusOK {
		t.Fatalf("status=%d after enabling again", w.Code)
	}
	if c.hits != 2 {
		t.Fatalf("hits=%d, want the two dispatched reads", c.hits)
	}
}

func TestEnablingAnAlreadyEnabledCapabilityIsAConflict(t *testing.T) {
	c := &fakeCapability{id: "notes"}
	h := handlerWithCapability(t, c)
	w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/notes/enable", true)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (body=%s)", w.Code, w.Body.String())
	}
}

func TestUnknownCapabilityStateChangeIsAConflict(t *testing.T) {
	c := &fakeCapability{id: "notes"}
	h := handlerWithCapability(t, c)
	w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/ghost/disable", true)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (body=%s)", w.Code, w.Body.String())
	}
}

func TestCapabilityStateChangeRejectsOtherMethods(t *testing.T) {
	c := &fakeCapability{id: "notes"}
	h := handlerWithCapability(t, c)
	w := capabilityRequest(t, h, http.MethodGet, "/api/plugins/notes/disable", false)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405 (body=%s)", w.Code, w.Body.String())
	}
}

// A kernel that has no capabilities configured still answers: the state list is
// empty, and no path turns into a capability route.
func TestNoCapabilitiesConfigured(t *testing.T) {
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	h := New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"})

	w := capabilityRequest(t, h, http.MethodGet, "/api/state", false)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var state struct {
		Capabilities []any `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Capabilities) != 0 {
		t.Fatalf("capabilities=%+v, want an empty list", state.Capabilities)
	}
	if w := capabilityRequest(t, h, http.MethodGet, "/api/notes", false); w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 without a registry", w.Code)
	}
}

// 用真实能力配真实内核走一遍：面板样式表必须能从能力自己的路由真的拿到，而且
// 响应上仍然挂着那条严格 CSP —— 面板样式能生效，靠的是"同源样式表"，不是放行
// 内联样式。这条路径一旦退回注入 <style>，真实服务下面板就是无样式的。
func TestTheMemoryPanelStylesheetIsReachableThroughTheKernel(t *testing.T) {
	capability, err := memory.New(filepath.Join(t.TempDir(), ".runtime"))
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	reg := plugin.NewRegistry(plugin.PermissionStateWrite)
	if err := reg.Register(capability); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Enable(memory.PluginID); err != nil {
		t.Fatalf("enable: %v", err)
	}
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	h := New(p, fakeRunner{}, newTestStore(t),
		Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"},
		WithCapabilities(reg))

	w := capabilityRequest(t, h, http.MethodGet, memory.PanelStylePath, false)
	if w.Code != http.StatusOK {
		t.Fatalf("stylesheet status=%d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("stylesheet content-type=%q, want a CSS type", ct)
	}
	if strings.TrimSpace(w.Body.String()) == "" {
		t.Fatal("the stylesheet the kernel served is empty")
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("the strict CSP must stay the policy this asset lives under: %q", csp)
	}
	if w := capabilityRequest(t, h, http.MethodGet, memory.PanelEntryPath, false); w.Code != http.StatusOK {
		t.Fatalf("panel module status=%d, want 200", w.Code)
	}
	// 停用能力，样式表和模块一起下线：面板样式不留在服务里。
	if w := capabilityRequest(t, h, http.MethodPost, "/api/plugins/memory/disable", true); w.Code != http.StatusOK {
		t.Fatalf("disable status=%d (body=%s)", w.Code, w.Body.String())
	}
	if w := capabilityRequest(t, h, http.MethodGet, memory.PanelStylePath, false); w.Code != http.StatusNotFound {
		t.Fatalf("a disabled capability still served its stylesheet: %d", w.Code)
	}
}
