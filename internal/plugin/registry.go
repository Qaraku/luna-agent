package plugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// idPattern 约束插件 ID：小写字母开头，只含小写字母、数字与连字符，总长不超过 32。
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// stateNamespacePattern 约束状态目录名：与插件 ID 同形，但允许一个前导点。
// 前导点是为了沿用仓库既有的 .runtime 约定（默认状态根就是仓库根），让认领该目录的
// 插件数据文件路径保持不变，不需要数据迁移。它是相对状态根的目录名，因此必须显式
// 拒绝 ".." 这类越界写法与路径分隔符 —— 见 checkStateNamespace。
var stateNamespacePattern = regexp.MustCompile(`^\.?[a-z][a-z0-9-]{0,31}$`)

// routePrefixPrefix 是所有 route-prefix claim 必须带的前缀。
const routePrefixPrefix = "/api/"

// Entry 是注册表里的一个插件记录。Err 保存最近一次生命周期操作失败的原因；
// 状态为 StateFailed 时它非空。Plugin 与 Descriptor 都是注册时的快照。
type Entry struct {
	Plugin     Plugin
	Descriptor Descriptor
	State      State
	Err        error
}

// claimKey 是 claim 在全库范围内的唯一键。
type claimKey struct {
	Kind ClaimKind
	ID   string
}

// contribKey 是同一个描述符内部贡献项的去重键。
type contribKey struct {
	Kind ContributionKind
	ID   string
}

// Registry 是插件清单的唯一权威：登记时校验描述符，之后回答“谁启用了、谁占用了什么”。
// 所有方法可并发调用（将来会从 HTTP 处理器里读写）。
type Registry struct {
	mu sync.RWMutex

	// grants 按授权时的登记顺序保存，granted 提供 O(1) 判定。
	grants  []PermissionKind
	granted map[PermissionKind]bool

	// entries 按登记顺序保存；index 是 ID 到 entries 下标的索引。
	entries []Entry
	index   map[string]int

	// claims 记录 (Kind, ID) 的当前占用者。
	claims map[claimKey]string
}

// NewRegistry 建立一个授权表为 grants 的注册表；不传参数表示默认无授权，
// 即任何 Permissions 声明都会被拒绝。
func NewRegistry(grants ...PermissionKind) *Registry {
	r := &Registry{
		granted: make(map[PermissionKind]bool, len(grants)),
		index:   make(map[string]int),
		claims:  make(map[claimKey]string),
	}
	for _, g := range grants {
		if g == "" || r.granted[g] {
			continue
		}
		r.granted[g] = true
		r.grants = append(r.grants, g)
	}
	return r
}

// Register 校验并登记一个插件。校验失败时注册表保持原样：不占用 ID，也不占用
// 该描述符声明过的任何 claim。
func (r *Registry) Register(p Plugin) error {
	if p == nil {
		return errors.New("plugin must not be nil")
	}
	d := p.Descriptor()

	// 先取授权表快照，再在锁外做纯校验与一致性校验（后者要调用插件方法）。
	r.mu.RLock()
	grants := make(map[PermissionKind]bool, len(r.granted))
	for k := range r.granted {
		grants[k] = true
	}
	r.mu.RUnlock()

	if err := validateDescriptor(d, grants); err != nil {
		return err
	}
	if err := checkConsistency(p, d); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.index[d.ID]; ok {
		return fmt.Errorf("plugin %q is already registered", d.ID)
	}
	pending := make(map[claimKey]bool, len(d.Claims))
	for _, c := range d.Claims {
		k := claimKey{Kind: c.Kind, ID: c.ID}
		if pending[k] {
			return fmt.Errorf("plugin %q declares claim %s %q twice", d.ID, c.Kind, c.ID)
		}
		pending[k] = true
		if owner, ok := r.claims[k]; ok {
			return fmt.Errorf("claim %s %q is already claimed by plugin %q", c.Kind, c.ID, owner)
		}
	}

	// 全部通过后才落库，保证失败不留痕。
	r.index[d.ID] = len(r.entries)
	r.entries = append(r.entries, Entry{
		Plugin:     p,
		Descriptor: cloneDescriptor(d),
		State:      StateRegistered,
	})
	for k := range pending {
		r.claims[k] = d.ID
	}
	return nil
}

// Enable 按生命周期状态机启用一个 builtin 插件。
func (r *Registry) Enable(id string) error { return r.apply(id, EventEnable) }

// Disable 按生命周期状态机停用一个 builtin 插件。
func (r *Registry) Disable(id string) error { return r.apply(id, EventDisable) }

// apply 是 Enable/Disable 的共同实现：非 builtin 形态与“已处于目标状态”都报错，
// 不静默成功；迁移被拒时记录原因并把插件标记为 failed。
func (r *Registry) apply(id string, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	i, ok := r.index[id]
	if !ok {
		return fmt.Errorf("plugin %q is not registered", id)
	}
	entry := &r.entries[i]
	if entry.Descriptor.Deployment != DeploymentBuiltin {
		return fmt.Errorf("plugin %q is a %s plugin: this registry can only enable or disable builtin plugins",
			id, entry.Descriptor.Deployment)
	}
	if e == EventEnable && entry.State == StateEnabled {
		return fmt.Errorf("plugin %q is already enabled", id)
	}
	if e == EventDisable && entry.State == StateDisabled {
		return fmt.Errorf("plugin %q is already disabled", id)
	}

	next, err := Transition(entry.State, e)
	if err != nil {
		entry.State = StateFailed
		entry.Err = err
		return err
	}
	entry.State = next
	entry.Err = nil
	return nil
}

// Entry 按 ID 返回副本与是否存在的标记。
func (r *Registry) Entry(id string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	i, ok := r.index[id]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(r.entries[i]), true
}

// Entries 按登记顺序返回所有插件的副本。
func (r *Registry) Entries() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, cloneEntry(e))
	}
	return out
}

// Enabled 按登记顺序返回处于 enabled 状态的插件副本。
func (r *Registry) Enabled() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.State == StateEnabled {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

// Grants 按 NewRegistry 的授权顺序返回授权表副本。
func (r *Registry) Grants() []PermissionKind {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return append([]PermissionKind(nil), r.grants...)
}

// validateDescriptor 校验描述符自身：标识形状、部署形态、贡献与 claim 的格式，
// 以及权限是否在授权表内。
func validateDescriptor(d Descriptor, grants map[PermissionKind]bool) error {
	if d.ID == "" {
		return errors.New("plugin id must not be empty")
	}
	if !idPattern.MatchString(d.ID) {
		return fmt.Errorf("plugin id %q must match %s", d.ID, idPattern.String())
	}
	if d.Title == "" {
		return fmt.Errorf("plugin %q must have a non-empty title", d.ID)
	}
	switch d.Deployment {
	case DeploymentBuiltin, DeploymentProcess, DeploymentBrowser:
	default:
		return fmt.Errorf("plugin %q has unknown deployment %q", d.ID, d.Deployment)
	}
	if err := validateContributions(d); err != nil {
		return err
	}
	if err := validateClaims(d); err != nil {
		return err
	}
	return validatePermissions(d, grants)
}

// validateContributions 校验贡献项：种类已知、ID 非空、(Kind,ID) 不重复。
func validateContributions(d Descriptor) error {
	seen := make(map[contribKey]bool, len(d.Contributions))
	for _, c := range d.Contributions {
		switch c.Kind {
		case ContributionTool, ContributionContext, ContributionRoute, ContributionPanel:
		default:
			return fmt.Errorf("plugin %q declares unknown contribution kind %q", d.ID, c.Kind)
		}
		if c.ID == "" {
			return fmt.Errorf("plugin %q declares a %s contribution with an empty id", d.ID, c.Kind)
		}
		k := contribKey{Kind: c.Kind, ID: c.ID}
		if seen[k] {
			return fmt.Errorf("plugin %q declares contribution %s %q twice", d.ID, c.Kind, c.ID)
		}
		seen[k] = true
	}
	return nil
}

// validateClaims 校验 claim 的形状。跨插件的占用冲突在 Register 落库前检查。
func validateClaims(d Descriptor) error {
	for _, c := range d.Claims {
		switch c.Kind {
		case ClaimRoutePrefix, ClaimPanel, ClaimStateNamespace:
		default:
			return fmt.Errorf("plugin %q declares unknown claim kind %q", d.ID, c.Kind)
		}
		if c.ID == "" {
			return fmt.Errorf("plugin %q declares a %s claim with an empty id", d.ID, c.Kind)
		}
		switch c.Kind {
		case ClaimRoutePrefix:
			if !strings.HasPrefix(c.ID, routePrefixPrefix) || strings.HasSuffix(c.ID, "/") {
				return fmt.Errorf("plugin %q route-prefix claim %q must start with %s and must not end with /",
					d.ID, c.ID, routePrefixPrefix)
			}
		case ClaimStateNamespace:
			if err := checkStateNamespace(c.ID); err != nil {
				return fmt.Errorf("plugin %q state-namespace claim: %w", d.ID, err)
			}
		}
	}
	return nil
}

// checkStateNamespace 校验状态目录名。除形状外必须显式拒绝 ".." 与路径分隔符：
// 形状约束本身不足以让人看清意图，而该名字会拼进文件系统路径，越界必须先于形状报错。
func checkStateNamespace(id string) error {
	if strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("%q must not contain %q or a path separator", id, "..")
	}
	if !stateNamespacePattern.MatchString(id) {
		return fmt.Errorf("%q must match %s", id, stateNamespacePattern.String())
	}
	return nil
}

// validatePermissions 校验权限申请：种类必须在授权表内（未授权的能力直接拒绝），
// Detail 目前尚未支持，非空即报错。
func validatePermissions(d Descriptor, grants map[PermissionKind]bool) error {
	for _, p := range d.Permissions {
		if p.Kind == "" {
			return fmt.Errorf("plugin %q declares a permission with an empty kind", d.ID)
		}
		if !grants[p.Kind] {
			return fmt.Errorf("plugin %q requests permission %q, an ability this registry is not authorized to grant",
				d.ID, p.Kind)
		}
		if p.Detail != "" {
			return fmt.Errorf("plugin %q requests permission %q with detail %q, which is not supported yet",
				d.ID, p.Kind, p.Detail)
		}
	}
	return nil
}

// checkConsistency 校验声明与实现一致：实现了某个可选接口就必须声明至少一个对应
// 贡献项，声明了某类贡献却没有实现对应接口同样报错。暴露与声明的对应关系分两种：
// 工具、路由与面板的返回值在运行期是固定的，因此要求双向一致（声明了就必须暴露）；
// ContextProvider 按运行态取内容，声明项是它可能贡献的上限，因此只要求暴露项都有声明。
func checkConsistency(p Plugin, d Descriptor) error {
	declared := func(kind ContributionKind) map[string]bool {
		ids := make(map[string]bool)
		for _, c := range d.Contributions {
			if c.Kind == kind {
				ids[c.ID] = true
			}
		}
		return ids
	}

	if tp, ok := p.(ToolProvider); ok {
		ids := declared(ContributionTool)
		if len(ids) == 0 {
			return fmt.Errorf("plugin %q implements ToolProvider but declares no tool contribution", d.ID)
		}
		exposed := make(map[string]bool)
		for _, t := range tp.Tools() {
			name := t.Name()
			if !ids[name] {
				return fmt.Errorf("plugin %q exposes tool %q without a matching tool contribution", d.ID, name)
			}
			exposed[name] = true
		}
		if err := requireExposed(d, ContributionTool, exposed, "tool"); err != nil {
			return err
		}
	} else if len(declared(ContributionTool)) > 0 {
		return fmt.Errorf("plugin %q declares tool contributions but does not implement ToolProvider", d.ID)
	}

	if cp, ok := p.(ContextProvider); ok {
		ids := declared(ContributionContext)
		if len(ids) == 0 {
			return fmt.Errorf("plugin %q implements ContextProvider but declares no context contribution", d.ID)
		}
		blocks, err := cp.Contexts(context.Background())
		if err != nil {
			return fmt.Errorf("plugin %q failed to list contexts: %w", d.ID, err)
		}
		for _, b := range blocks {
			if !ids[b.ID] {
				return fmt.Errorf("plugin %q exposes context %q without a matching context contribution", d.ID, b.ID)
			}
		}
	} else if len(declared(ContributionContext)) > 0 {
		return fmt.Errorf("plugin %q declares context contributions but does not implement ContextProvider", d.ID)
	}

	if rp, ok := p.(RouteProvider); ok {
		ids := declared(ContributionRoute)
		if len(ids) == 0 {
			return fmt.Errorf("plugin %q implements RouteProvider but declares no route contribution", d.ID)
		}
		exposed := make(map[string]bool)
		for _, route := range rp.Routes() {
			path := route.Path()
			if !ids[path] {
				return fmt.Errorf("plugin %q exposes route %q without a matching route contribution", d.ID, path)
			}
			exposed[path] = true
		}
		if err := requireExposed(d, ContributionRoute, exposed, "route"); err != nil {
			return err
		}
	} else if len(declared(ContributionRoute)) > 0 {
		return fmt.Errorf("plugin %q declares route contributions but does not implement RouteProvider", d.ID)
	}

	if pp, ok := p.(PanelProvider); ok {
		ids := declared(ContributionPanel)
		if len(ids) == 0 {
			return fmt.Errorf("plugin %q implements PanelProvider but declares no panel contribution", d.ID)
		}
		exposed := make(map[string]bool)
		for _, panel := range pp.Panels() {
			if !ids[panel.ID] {
				return fmt.Errorf("plugin %q exposes panel %q without a matching panel contribution", d.ID, panel.ID)
			}
			exposed[panel.ID] = true
		}
		if err := requireExposed(d, ContributionPanel, exposed, "panel"); err != nil {
			return err
		}
	} else if len(declared(ContributionPanel)) > 0 {
		return fmt.Errorf("plugin %q declares panel contributions but does not implement PanelProvider", d.ID)
	}

	return nil
}

// requireExposed 断言静态贡献项声明了就一定暴露。工具、路由与面板的接口返回值在
// 运行期不变，所以“声明了却没有对应实现”是描述符在说谎，不是运行期子集；只有
// ContextProvider 按运行态取内容，允许返回声明项的子集。
func requireExposed(d Descriptor, kind ContributionKind, exposed map[string]bool, what string) error {
	for _, c := range d.Contributions {
		if c.Kind == kind && !exposed[c.ID] {
			return fmt.Errorf("plugin %q declares %s contribution %q but exposes no such %s", d.ID, kind, c.ID, what)
		}
	}
	return nil
}

// cloneEntry 复制一条记录。描述符的切片一并复制，使 Entries/Entry 的调用方改不动
// 注册表的内部状态。
func cloneEntry(e Entry) Entry {
	return Entry{
		Plugin:     e.Plugin,
		Descriptor: cloneDescriptor(e.Descriptor),
		State:      e.State,
		Err:        e.Err,
	}
}

// cloneDescriptor 浅复制描述符并深复制其切片。
func cloneDescriptor(d Descriptor) Descriptor {
	return Descriptor{
		ID:            d.ID,
		Title:         d.Title,
		Deployment:    d.Deployment,
		Contributions: append([]Contribution(nil), d.Contributions...),
		Claims:        append([]Claim(nil), d.Claims...),
		Permissions:   append([]Permission(nil), d.Permissions...),
	}
}
