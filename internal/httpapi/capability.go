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
	case providerPath:
		// Saving a provider writes the file a run calls through, so it is a
		// mutation in the same way starting a run is. A read method to this
		// path is a wrong-method request and is answered 405.
		return !isReadMethod(method)
	case providerModelsPath:
		// Asking the endpoint which models it serves spends this
		// installation's key on an outbound call, so a foreign page must not be
		// able to make Luna do it.
		return true
	case writeDirsPath:
		// Deciding which directories may be written in is what the write tools
		// obey, so it is a mutation in the same way saving a provider is. A read
		// method to this path is a wrong-method request and is answered 405.
		return !isReadMethod(method)
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
	if strings.HasPrefix(path, skillsStatePrefix) {
		// Turning a skill off writes to the user's own settings file, so it is
		// a mutation in the same way starting a run is. A read method to this
		// path is a wrong-method request and is answered 405.
		return !isReadMethod(method)
	}
	if _, ok := sessionModelPath(path); ok {
		// Choosing a session's model changes what its next runs do, so it is a
		// mutation in the same way starting a run is and carries the same Origin
		// requirement. A read method to this path is a wrong-method request.
		return !isReadMethod(method)
	}
	if _, ok := sessionWorkspacePath(path); ok {
		// Binding a session to a workspace writes a config record, which is a
		// mutation for the same reason choosing its model is: it changes what
		// the session's next runs are set up with.
		return !isReadMethod(method)
	}
	if path == workspacesPath {
		// GET reads the list; POST defines a workspace and rewrites the file
		// the user owns. Only the write carries the Origin requirement, the
		// same split /api/plugins/{id} uses between reading and changing.
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

// CapabilityPreference 记录用户对能力启停的选择。装配根提供它：选择属于用户，存在哪个
// 文件里是它的事。没有一个时，启停只影响这个进程（现状），行为不变。
//
// SetEnabled 只负责"把这次选择写下来"：要么写成了，要么报错，没有第三种结果。它不判断
// 这个 id 是否存在、也不判断这次迁移是否允许——那是注册表的事，见 setPluginState。
type CapabilityPreference interface {
	// SetEnabled 记下用户希望这个能力处于 enabled 状态。返回错误表示这次选择没有
	// 被记下，此时什么也没有改变。
	SetEnabled(id string, enabled bool) error
}

// WithCapabilityPreference 提供记下用户选择的地方。没有它时，启停只改这个进程的运行
// 态、不落盘，与这个 seam 出现之前完全一致。
func WithCapabilityPreference(pref CapabilityPreference) Option {
	return func(s *Server) { s.capabilityPref = pref }
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
	enabled := action == "enable"
	// 先写选择、再改运行态，顺序不能反。
	//
	// 这两样东西的寿命不一样：选择是持久的，它决定下次启动时这个能力在不在；运行态
	// 只属于这个进程。反过来先改运行态，写盘失败就会留下"进程里已经是新状态、重启后
	// 又退回旧状态"的结果，而请求回的是失败——"请求失败"与"已经改了"于是分不开，
	// 用户既不敢重试也不能相信那句失败。先写盘时失败只可能停在写盘这一步，运行态一
	// 个字节没动，500 就等于"什么都没发生"，重试是安全的。
	//
	// 注册表自己的拒绝（未知 id、不允许的迁移）发生在这之后，仍按现在那样报 409，
	// 运行态同样没被碰过：写在文件里的只是用户这次的要求，这一层的职责是把它记下来。
	if s.capabilityPref != nil {
		if err := s.capabilityPref.SetEnabled(id, enabled); err != nil {
			fail(w, 500, err)
			return
		}
	}
	var err error
	if enabled {
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
