package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// pluginStatePrefix is the kernel's own namespace for changing a capability's
// state. It is not a capability route: the routes a capability contributes are
// its own data and behavior, while whether it is in service is the kernel's
// business.
const pluginStatePrefix = "/api/plugins/"

// pluginStatePath splits /api/plugins/{id}/{enable|disable}.
func pluginStatePath(path string) (id, action string, ok bool) {
	rest, found := strings.CutPrefix(path, pluginStatePrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" {
		return "", "", false
	}
	if parts[1] != "enable" && parts[1] != "disable" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// requiresOrigin reports whether a request has to carry the bound Origin: every
// kernel mutation, and every capability route that is not a plain read.
//
// A path a capability serves only with another method is not a mutation of this
// request's making: that request is a 405 and needs no Origin, which keeps the
// behaviour of a wrong-method request the same as before capabilities existed.
func (s *Server) requiresOrigin(method, path string) bool {
	switch path {
	case "/api/reload", "/api/runs":
		return true
	}
	if _, ok := cancelRunPath(path); ok {
		// Stopping the active run is a kernel mutation in the same way starting
		// one is, so it carries the same Origin requirement as POST /api/runs.
		return true
	}
	if strings.HasPrefix(path, pluginStatePrefix) {
		// A state change is a mutation; a request with a read method to the same
		// path is a wrong-method request and is answered 405.
		return !isReadMethod(method)
	}
	if _, ok := sessionModelPath(path); ok {
		// Choosing a session's model changes what its next runs do, so it is a
		// mutation in the same way starting a run is and carries the same Origin
		// requirement. A read method to this path is a wrong-method request.
		return !isReadMethod(method)
	}
	route, _, _ := s.capabilityRoute(method, path)
	return route != nil && !isReadMethod(route.Method())
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// capabilityRoute resolves a route an enabled capability contributes. It
// returns the route when one serves this path and method; when no route serves
// the path, known is false; when a route serves the path with another method,
// allowed carries that method so the caller can answer 405 with it.
func (s *Server) capabilityRoute(method, path string) (route plugin.Route, allowed string, known bool) {
	if s.capabilities == nil {
		return nil, "", false
	}
	for _, entry := range s.capabilities.Enabled() {
		provider, ok := entry.Plugin.(plugin.RouteProvider)
		if !ok {
			continue
		}
		for _, candidate := range provider.Routes() {
			if candidate.Path() != path {
				continue
			}
			known = true
			if candidate.Method() == method {
				return candidate, candidate.Method(), true
			}
			allowed = candidate.Method()
		}
	}
	return nil, allowed, known
}

// capabilityView is one capability as the browser sees it: what it declares and
// whether it is in service. It never carries the capability's own data — the
// kernel does not have any to show.
type capabilityView struct {
	ID            string             `json:"id"`
	Title         string             `json:"title"`
	Deployment    string             `json:"deployment"`
	State         string             `json:"state"`
	Contributions []contributionView `json:"contributions"`
	Claims        []claimView        `json:"claims"`
	Permissions   []permissionView   `json:"permissions"`
	Panels        []panelView        `json:"panels"`
	Error         string             `json:"error,omitempty"`
}

// panelView is a browser panel a capability contributes: what the host needs to
// render an entry for it and mount the module behind it. The module itself comes
// from the capability's own route.
type panelView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Entry string `json:"entry"`
}

type contributionView struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	BudgetBytes int    `json:"budget_bytes,omitempty"`
}

type claimView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type permissionView struct {
	Kind string `json:"kind"`
}

// capabilityViews is the whole registry, in registration order.
func capabilityViews(reg *plugin.Registry) []capabilityView {
	if reg == nil {
		return []capabilityView{}
	}
	entries := reg.Entries()
	views := make([]capabilityView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, capabilityViewOf(entry))
	}
	return views
}

func capabilityViewOf(entry plugin.Entry) capabilityView {
	view := capabilityView{
		ID:            entry.Descriptor.ID,
		Title:         entry.Descriptor.Title,
		Deployment:    string(entry.Descriptor.Deployment),
		State:         string(entry.State),
		Contributions: make([]contributionView, 0, len(entry.Descriptor.Contributions)),
		Claims:        make([]claimView, 0, len(entry.Descriptor.Claims)),
		Permissions:   make([]permissionView, 0, len(entry.Descriptor.Permissions)),
	}
	for _, c := range entry.Descriptor.Contributions {
		view.Contributions = append(view.Contributions, contributionView{Kind: string(c.Kind), ID: c.ID, BudgetBytes: c.BudgetBytes})
	}
	for _, c := range entry.Descriptor.Claims {
		view.Claims = append(view.Claims, claimView{Kind: string(c.Kind), ID: c.ID})
	}
	for _, p := range entry.Descriptor.Permissions {
		view.Permissions = append(view.Permissions, permissionView{Kind: string(p.Kind)})
	}
	view.Panels = make([]panelView, 0, len(entry.Descriptor.Contributions))
	if provider, ok := entry.Plugin.(plugin.PanelProvider); ok {
		for _, panel := range provider.Panels() {
			view.Panels = append(view.Panels, panelView{ID: panel.ID, Title: panel.Title, Entry: panel.Entry})
		}
	}
	if entry.Err != nil {
		view.Error = entry.Err.Error()
	}
	return view
}

// setPluginState enables or disables a built-in capability.
//
// Disabling takes the capability's tools, context blocks, routes and panels out
// of service together, because they are one boundary; it never touches the data
// the capability keeps. Removing data is a separate, explicit operation.
func (s *Server) setPluginState(w http.ResponseWriter, id, action string) {
	if s.capabilities == nil {
		fail(w, 500, fmt.Errorf("no capability registry is configured"))
		return
	}
	var err error
	if action == "enable" {
		err = s.capabilities.Enable(id)
	} else {
		err = s.capabilities.Disable(id)
	}
	if err != nil {
		// A rejected transition is a conflict with the current state, not a bad
		// request: the caller asked for something the state machine does not
		// allow from here.
		fail(w, 409, err)
		return
	}
	entry, ok := s.capabilities.Entry(id)
	if !ok {
		fail(w, 500, fmt.Errorf("capability %q is no longer registered", id))
		return
	}
	s.addEvent("capability_"+action, fmt.Sprintf("capability %q is now %s", id, entry.State))
	send(w, 200, capabilityViewOf(entry))
}
