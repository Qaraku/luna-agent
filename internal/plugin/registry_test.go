package plugin

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	jsonschema "github.com/eino-contrib/jsonschema"
)

// ---- 测试替身 ----

// barePlugin 只实现 Plugin：用于只关心描述符校验的用例。
type barePlugin struct{ d Descriptor }

func (p barePlugin) Descriptor() Descriptor { return p.d }

// toolPlugin 实现 Plugin 与 ToolProvider。
type toolPlugin struct {
	d     Descriptor
	tools []Tool
}

func (p toolPlugin) Descriptor() Descriptor { return p.d }
func (p toolPlugin) Tools() []Tool          { return p.tools }

// contextPlugin 实现 Plugin 与 ContextProvider。
type contextPlugin struct {
	d    Descriptor
	list []ContextBlock
	err  error
}

func (p contextPlugin) Descriptor() Descriptor { return p.d }
func (p contextPlugin) Contexts(context.Context) ([]ContextBlock, error) {
	return p.list, p.err
}

// routePlugin 实现 Plugin 与 RouteProvider。
type routePlugin struct {
	d      Descriptor
	routes []Route
}

func (p routePlugin) Descriptor() Descriptor { return p.d }
func (p routePlugin) Routes() []Route        { return p.routes }

// panelPlugin 实现 Plugin 与 PanelProvider。
type panelPlugin struct {
	d      Descriptor
	panels []Panel
}

func (p panelPlugin) Descriptor() Descriptor { return p.d }
func (p panelPlugin) Panels() []Panel        { return p.panels }

// fullPlugin 同时实现四个可选接口，用于一次性走完四类一致性校验。
type fullPlugin struct {
	d      Descriptor
	tools  []Tool
	blocks []ContextBlock
	routes []Route
	panels []Panel
}

func (p fullPlugin) Descriptor() Descriptor { return p.d }
func (p fullPlugin) Tools() []Tool          { return p.tools }
func (p fullPlugin) Contexts(context.Context) ([]ContextBlock, error) {
	return p.blocks, nil
}
func (p fullPlugin) Routes() []Route { return p.routes }
func (p fullPlugin) Panels() []Panel { return p.panels }

// fakeTool 满足 Tool 契约（Schema 的签名也一并被编译期校验）。
type fakeTool struct{ name string }

func (t fakeTool) Name() string                                   { return t.name }
func (t fakeTool) Description() string                            { return "test tool " + t.name }
func (t fakeTool) Schema() *jsonschema.Schema                     { return &jsonschema.Schema{} }
func (t fakeTool) Invoke(context.Context, string) (string, error) { return t.name, nil }

// fakeRoute 满足 Route 契约。
type fakeRoute struct{ method, path string }

func (r fakeRoute) Method() string                               { return r.method }
func (r fakeRoute) Path() string                                 { return r.path }
func (r fakeRoute) ServeHTTP(http.ResponseWriter, *http.Request) {}

// desc 造一个最小可用描述符。
func desc(id string, deployment Deployment) Descriptor {
	return Descriptor{ID: id, Title: "Test " + id, Deployment: deployment}
}

func withContrib(d Descriptor, cs ...Contribution) Descriptor {
	d.Contributions = cs
	return d
}

func withClaims(d Descriptor, cs ...Claim) Descriptor {
	d.Claims = cs
	return d
}

func withPerms(d Descriptor, ps ...Permission) Descriptor {
	d.Permissions = ps
	return d
}

// applyEvent 把生命周期事件翻译成注册表方法调用。
func applyEvent(r *Registry, id string, e Event) error {
	if e == EventEnable {
		return r.Enable(id)
	}
	return r.Disable(id)
}

// ---- Register：接受 ----

func TestRegisterAccepts(t *testing.T) {
	tests := []struct {
		name   string
		grants []PermissionKind
		p      Plugin
	}{
		{
			name: "minimal builtin descriptor",
			p:    barePlugin{d: desc("luna-memory", DeploymentBuiltin)},
		},
		{
			name: "id at the 32 character boundary",
			p:    barePlugin{d: desc("a"+strings.Repeat("b", 31), DeploymentBuiltin)},
		},
		{
			name: "id with digits and hyphens",
			p:    barePlugin{d: desc("luna-tool-2", DeploymentBuiltin)},
		},
		{
			name: "process deployment registers",
			p:    barePlugin{d: desc("luna-process", DeploymentProcess)},
		},
		{
			name: "browser deployment registers",
			p:    barePlugin{d: desc("luna-browser", DeploymentBrowser)},
		},
		{
			name: "tool provider consistent with its declaration",
			p: toolPlugin{
				d:     withContrib(desc("luna-tools", DeploymentBuiltin), Contribution{Kind: ContributionTool, ID: "echo"}),
				tools: []Tool{fakeTool{name: "echo"}},
			},
		},
		{
			name: "context provider consistent with its declaration",
			p: contextPlugin{
				d:    withContrib(desc("luna-context", DeploymentBuiltin), Contribution{Kind: ContributionContext, ID: "docs"}),
				list: []ContextBlock{{ID: "docs", Kind: ContextReference, Text: "reference data"}},
			},
		},
		{
			// 与工具/路由/面板不同，上下文按运行态取内容：声明项是它可以贡献的上限，
			// 某一轮一条都不贡献也是合法的。
			name: "context provider exposing a subset of its declared contributions",
			p: contextPlugin{
				d: withContrib(desc("luna-context", DeploymentBuiltin),
					Contribution{Kind: ContributionContext, ID: "docs"},
					Contribution{Kind: ContributionContext, ID: "runtime"}),
				list: []ContextBlock{{ID: "docs", Kind: ContextReference, Text: "reference data"}},
			},
		},
		{
			name: "route provider consistent with its declaration",
			p: routePlugin{
				d: withClaims(
					withContrib(desc("luna-routes", DeploymentBuiltin), Contribution{Kind: ContributionRoute, ID: "/api/luna/echo"}),
					Claim{Kind: ClaimRoutePrefix, ID: "/api/luna"},
				),
				routes: []Route{fakeRoute{method: http.MethodPost, path: "/api/luna/echo"}},
			},
		},
		{
			name: "panel provider consistent with its declaration",
			p: panelPlugin{
				d: withClaims(
					withContrib(desc("luna-panels", DeploymentBuiltin), Contribution{Kind: ContributionPanel, ID: "memory"}),
					Claim{Kind: ClaimPanel, ID: "memory"},
				),
				panels: []Panel{{ID: "memory", Title: "Memory", Entry: "/plugins/ui/memory/index.js"}},
			},
		},
		{
			name: "all four provider kinds in one descriptor",
			p: fullPlugin{
				d: withContrib(desc("luna-full", DeploymentBuiltin),
					Contribution{Kind: ContributionTool, ID: "echo"},
					Contribution{Kind: ContributionContext, ID: "docs"},
					Contribution{Kind: ContributionRoute, ID: "/api/luna/full"},
					Contribution{Kind: ContributionPanel, ID: "full"},
				),
				tools:  []Tool{fakeTool{name: "echo"}},
				blocks: []ContextBlock{{ID: "docs", Kind: ContextReference, Text: "reference data"}},
				routes: []Route{fakeRoute{method: http.MethodGet, path: "/api/luna/full"}},
				panels: []Panel{{ID: "full", Title: "Full"}},
			},
		},
		{
			name: "state namespace claim with the existing .runtime convention",
			p: barePlugin{d: withClaims(desc("luna-state", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: ".runtime"})},
		},
		{
			name: "state namespace claim without a leading dot",
			p: barePlugin{d: withClaims(desc("luna-state", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: "luna-state"})},
		},
		{
			name:   "granted state.write permission with an empty detail",
			grants: []PermissionKind{PermissionStateWrite},
			p: barePlugin{d: withPerms(desc("luna-writer", DeploymentBuiltin),
				Permission{Kind: PermissionStateWrite})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry(tt.grants...)
			if err := r.Register(tt.p); err != nil {
				t.Fatalf("Register: unexpected error %v", err)
			}
			id := tt.p.Descriptor().ID
			got, ok := r.Entry(id)
			if !ok {
				t.Fatalf("plugin %q is missing after Register", id)
			}
			if got.State != StateRegistered {
				t.Fatalf("state = %q, want %q", got.State, StateRegistered)
			}
			if got.Err != nil {
				t.Fatalf("Err = %v, want nil", got.Err)
			}
		})
	}
}

// ---- Register：拒绝 ----

func TestRegisterRejects(t *testing.T) {
	tests := []struct {
		name    string
		grants  []PermissionKind
		pre     []Plugin // 先成功登记的插件
		target  Plugin   // 待登记插件
		wantErr string   // 期望错误信息包含的子串
	}{
		{
			name:    "nil plugin",
			target:  nil,
			wantErr: "must not be nil",
		},
		{
			name:    "empty id",
			target:  barePlugin{d: Descriptor{ID: "", Title: "No id", Deployment: DeploymentBuiltin}},
			wantErr: "plugin id must not be empty",
		},
		{
			name:    "id with an upper case letter",
			target:  barePlugin{d: desc("Luna", DeploymentBuiltin)},
			wantErr: "must match",
		},
		{
			name:    "id starting with a digit",
			target:  barePlugin{d: desc("1luna", DeploymentBuiltin)},
			wantErr: "must match",
		},
		{
			name:    "id with an underscore",
			target:  barePlugin{d: desc("luna_agent", DeploymentBuiltin)},
			wantErr: "must match",
		},
		{
			name:    "id longer than 32 characters",
			target:  barePlugin{d: desc(strings.Repeat("a", 33), DeploymentBuiltin)},
			wantErr: "must match",
		},
		{
			name:    "empty title",
			target:  barePlugin{d: Descriptor{ID: "luna", Deployment: DeploymentBuiltin}},
			wantErr: "non-empty title",
		},
		{
			name:    "unknown deployment",
			target:  barePlugin{d: desc("luna", Deployment("wasm"))},
			wantErr: `unknown deployment "wasm"`,
		},
		{
			name:    "duplicate id",
			pre:     []Plugin{barePlugin{d: desc("luna", DeploymentBuiltin)}},
			target:  barePlugin{d: desc("luna", DeploymentProcess)},
			wantErr: `plugin "luna" is already registered`,
		},
		{
			name: "contribution with an empty id",
			target: barePlugin{d: withContrib(desc("luna", DeploymentBuiltin),
				Contribution{Kind: ContributionTool, ID: ""})},
			wantErr: "with an empty id",
		},
		{
			name: "duplicate contribution",
			target: toolPlugin{
				d: withContrib(desc("luna", DeploymentBuiltin),
					Contribution{Kind: ContributionTool, ID: "echo"},
					Contribution{Kind: ContributionTool, ID: "echo"}),
				tools: []Tool{fakeTool{name: "echo"}},
			},
			wantErr: "declares contribution tool \"echo\" twice",
		},
		{
			name: "unknown contribution kind",
			target: barePlugin{d: withContrib(desc("luna", DeploymentBuiltin),
				Contribution{Kind: ContributionKind("hook"), ID: "on-start"})},
			wantErr: `unknown contribution kind "hook"`,
		},
		{
			name: "tool provider without a tool contribution",
			target: toolPlugin{
				d:     desc("luna", DeploymentBuiltin),
				tools: []Tool{fakeTool{name: "echo"}},
			},
			wantErr: "implements ToolProvider but declares no tool contribution",
		},
		{
			name: "tool exposed without a matching contribution",
			target: toolPlugin{
				d:     withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionTool, ID: "echo"}),
				tools: []Tool{fakeTool{name: "echo"}, fakeTool{name: "ls"}},
			},
			wantErr: `exposes tool "ls" without a matching tool contribution`,
		},
		{
			name:    "tool contribution without a ToolProvider implementation",
			target:  barePlugin{d: withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionTool, ID: "echo"})},
			wantErr: "declares tool contributions but does not implement ToolProvider",
		},
		{
			name: "context provider without a context contribution",
			target: contextPlugin{
				d:    desc("luna", DeploymentBuiltin),
				list: []ContextBlock{{ID: "docs", Kind: ContextReference}},
			},
			wantErr: "implements ContextProvider but declares no context contribution",
		},
		{
			name: "context exposed without a matching contribution",
			target: contextPlugin{
				d:    withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionContext, ID: "notes"}),
				list: []ContextBlock{{ID: "docs", Kind: ContextReference}},
			},
			wantErr: `exposes context "docs" without a matching context contribution`,
		},
		{
			name:    "context contribution without a ContextProvider implementation",
			target:  barePlugin{d: withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionContext, ID: "docs"})},
			wantErr: "declares context contributions but does not implement ContextProvider",
		},
		{
			name: "context listing fails",
			target: contextPlugin{
				d:   withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionContext, ID: "docs"}),
				err: errors.New("boom"),
			},
			wantErr: "failed to list contexts",
		},
		{
			name: "route provider without a route contribution",
			target: routePlugin{
				d:      desc("luna", DeploymentBuiltin),
				routes: []Route{fakeRoute{method: http.MethodGet, path: "/api/luna"}},
			},
			wantErr: "implements RouteProvider but declares no route contribution",
		},
		{
			name: "route exposed without a matching contribution",
			target: routePlugin{
				d:      withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionRoute, ID: "/api/luna/other"}),
				routes: []Route{fakeRoute{method: http.MethodGet, path: "/api/luna"}},
			},
			wantErr: `exposes route "/api/luna" without a matching route contribution`,
		},
		{
			name:    "route contribution without a RouteProvider implementation",
			target:  barePlugin{d: withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionRoute, ID: "/api/luna"})},
			wantErr: "declares route contributions but does not implement RouteProvider",
		},
		{
			name: "panel provider without a panel contribution",
			target: panelPlugin{
				d:      desc("luna", DeploymentBuiltin),
				panels: []Panel{{ID: "memory"}},
			},
			wantErr: "implements PanelProvider but declares no panel contribution",
		},
		{
			name: "panel exposed without a matching contribution",
			target: panelPlugin{
				d:      withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionPanel, ID: "notes"}),
				panels: []Panel{{ID: "memory"}},
			},
			wantErr: `exposes panel "memory" without a matching panel contribution`,
		},
		{
			name:    "panel contribution without a PanelProvider implementation",
			target:  barePlugin{d: withContrib(desc("luna", DeploymentBuiltin), Contribution{Kind: ContributionPanel, ID: "memory"})},
			wantErr: "declares panel contributions but does not implement PanelProvider",
		},
		{
			// 工具、路由与面板的接口返回值在运行期不变，所以“声明了却没暴露”是
			// 描述符在说谎，而不是运行期子集。
			name: "declared tool contribution that is never exposed",
			target: toolPlugin{
				d: withContrib(desc("luna", DeploymentBuiltin),
					Contribution{Kind: ContributionTool, ID: "echo"},
					Contribution{Kind: ContributionTool, ID: "ghost"}),
				tools: []Tool{fakeTool{name: "echo"}},
			},
			wantErr: `declares tool contribution "ghost" but exposes no such tool`,
		},
		{
			name: "declared route contribution that is never exposed",
			target: routePlugin{
				d: withContrib(desc("luna", DeploymentBuiltin),
					Contribution{Kind: ContributionRoute, ID: "/api/luna/echo"},
					Contribution{Kind: ContributionRoute, ID: "/api/luna/ghost"}),
				routes: []Route{fakeRoute{method: http.MethodGet, path: "/api/luna/echo"}},
			},
			wantErr: `declares route contribution "/api/luna/ghost" but exposes no such route`,
		},
		{
			name: "declared panel contribution that is never exposed",
			target: panelPlugin{
				d: withContrib(desc("luna", DeploymentBuiltin),
					Contribution{Kind: ContributionPanel, ID: "memory"},
					Contribution{Kind: ContributionPanel, ID: "ghost"}),
				panels: []Panel{{ID: "memory", Title: "Memory", Entry: "/api/luna/memory.js"}},
			},
			wantErr: `declares panel contribution "ghost" but exposes no such panel`,
		},
		{
			name: "claim conflict on the same kind and id",
			pre: []Plugin{barePlugin{d: withClaims(desc("memory", DeploymentBuiltin),
				Claim{Kind: ClaimPanel, ID: "memory"})}},
			target: barePlugin{d: withClaims(desc("notes", DeploymentBuiltin),
				Claim{Kind: ClaimPanel, ID: "memory"})},
			wantErr: `claim panel "memory" is already claimed by plugin "memory"`,
		},
		{
			// 工具名是模型看到的名字，两个插件各贡献一个同名工具会让内核必须挑一个。
			name: "two plugins contribute a tool with the same name",
			pre: []Plugin{toolPlugin{
				d:     withContrib(desc("first", DeploymentBuiltin), Contribution{Kind: ContributionTool, ID: "echo"}),
				tools: []Tool{fakeTool{name: "echo"}},
			}},
			target: toolPlugin{
				d:     withContrib(desc("second", DeploymentBuiltin), Contribution{Kind: ContributionTool, ID: "echo"}),
				tools: []Tool{fakeTool{name: "echo"}},
			},
			wantErr: `contribution tool "echo" is already contributed by plugin "first"`,
		},
		{
			name: "two plugins contribute the same route path",
			pre: []Plugin{routePlugin{
				d:      withContrib(desc("first", DeploymentBuiltin), Contribution{Kind: ContributionRoute, ID: "/api/first/echo"}),
				routes: []Route{fakeRoute{method: http.MethodGet, path: "/api/first/echo"}},
			}},
			target: routePlugin{
				d:      withContrib(desc("second", DeploymentBuiltin), Contribution{Kind: ContributionRoute, ID: "/api/first/echo"}),
				routes: []Route{fakeRoute{method: http.MethodGet, path: "/api/first/echo"}},
			},
			wantErr: `contribution route "/api/first/echo" is already contributed by plugin "first"`,
		},
		{
			name: "two plugins contribute a panel with the same id",
			pre: []Plugin{panelPlugin{
				d:      withContrib(desc("first", DeploymentBuiltin), Contribution{Kind: ContributionPanel, ID: "notes"}),
				panels: []Panel{{ID: "notes", Title: "Notes", Entry: "/api/first/panel.js"}},
			}},
			target: panelPlugin{
				d:      withContrib(desc("second", DeploymentBuiltin), Contribution{Kind: ContributionPanel, ID: "notes"}),
				panels: []Panel{{ID: "notes", Title: "Notes", Entry: "/api/second/panel.js"}},
			},
			wantErr: `contribution panel "notes" is already contributed by plugin "first"`,
		},
		{
			name: "two plugins fight over the same route prefix",
			pre: []Plugin{barePlugin{d: withClaims(desc("memory-api", DeploymentBuiltin),
				Claim{Kind: ClaimRoutePrefix, ID: "/api/memory"})}},
			target: barePlugin{d: withClaims(desc("notes-api", DeploymentBuiltin),
				Claim{Kind: ClaimRoutePrefix, ID: "/api/memory"})},
			wantErr: `claim route-prefix "/api/memory" is already claimed`,
		},
		{
			name: "duplicate claim inside one descriptor",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimPanel, ID: "memory"},
				Claim{Kind: ClaimPanel, ID: "memory"})},
			wantErr: "declares claim panel \"memory\" twice",
		},
		{
			name:    "claim with an empty id",
			target:  barePlugin{d: withClaims(desc("luna", DeploymentBuiltin), Claim{Kind: ClaimPanel, ID: ""})},
			wantErr: "with an empty id",
		},
		{
			name: "unknown claim kind",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimKind("socket"), ID: "unix"})},
			wantErr: `unknown claim kind "socket"`,
		},
		{
			name: "route prefix outside /api",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimRoutePrefix, ID: "/memory"})},
			wantErr: "must start with /api/ and must not end with /",
		},
		{
			name: "route prefix with a trailing slash",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimRoutePrefix, ID: "/api/memory/"})},
			wantErr: "must start with /api/ and must not end with /",
		},
		{
			name: "bare /api route prefix",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimRoutePrefix, ID: "/api"})},
			wantErr: "must start with /api/ and must not end with /",
		},
		{
			name: "state namespace with an upper case letter",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: "Memory"})},
			wantErr: "must match",
		},
		{
			name: "state namespace that is a parent directory",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: ".."})},
			wantErr: "must not contain",
		},
		{
			name: "state namespace escaping through a path",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: "runtime/../etc"})},
			wantErr: "must not contain",
		},
		{
			name: "state namespace with a leading dot and a parent segment",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: ".runtime/.."})},
			wantErr: "must not contain",
		},
		{
			name: "state namespace with a backslash",
			target: barePlugin{d: withClaims(desc("luna", DeploymentBuiltin),
				Claim{Kind: ClaimStateNamespace, ID: `runtime\etc`})},
			wantErr: "must not contain",
		},
		{
			name:    "permission without a grant",
			target:  barePlugin{d: withPerms(desc("luna", DeploymentBuiltin), Permission{Kind: PermissionStateWrite})},
			wantErr: "an ability this registry is not authorized to grant",
		},
		{
			name:    "permission granted to another kind",
			grants:  []PermissionKind{PermissionKind("state.read")},
			target:  barePlugin{d: withPerms(desc("luna", DeploymentBuiltin), Permission{Kind: PermissionStateWrite})},
			wantErr: "not authorized to grant",
		},
		{
			name:    "permission with an empty kind",
			target:  barePlugin{d: withPerms(desc("luna", DeploymentBuiltin), Permission{})},
			wantErr: "permission with an empty kind",
		},
		{
			name:   "permission detail is not supported yet",
			grants: []PermissionKind{PermissionStateWrite},
			target: barePlugin{d: withPerms(desc("luna", DeploymentBuiltin),
				Permission{Kind: PermissionStateWrite, Detail: "only:/api/memory"})},
			wantErr: "not supported yet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry(tt.grants...)
			for _, p := range tt.pre {
				if err := r.Register(p); err != nil {
					t.Fatalf("pre-register %q: %v", p.Descriptor().ID, err)
				}
			}
			before := len(r.Entries())

			err := r.Register(tt.target)
			if err == nil {
				t.Fatalf("Register: expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Register error %q does not contain %q", err.Error(), tt.wantErr)
			}
			if got := len(r.Entries()); got != before {
				t.Fatalf("Entries = %d after a failed Register, want %d", got, before)
			}
		})
	}
}

// TestRegisterRejectionKeepsNoResidue 校验失败用例既不占用 ID，也不占用它声明过的 claim。
func TestRegisterRejectionKeepsNoResidue(t *testing.T) {
	r := NewRegistry()
	rejected := barePlugin{d: withClaims(desc("Bad", DeploymentBuiltin), Claim{Kind: ClaimPanel, ID: "memory"})}
	if err := r.Register(rejected); err == nil {
		t.Fatal("Register: expected the illegal id to be rejected")
	}
	if _, ok := r.Entry("Bad"); ok {
		t.Fatal("a rejected plugin is present in the registry")
	}
	if got := r.Entries(); len(got) != 0 {
		t.Fatalf("Entries = %d, want 0", len(got))
	}

	accepted := barePlugin{d: withClaims(desc("memory", DeploymentBuiltin), Claim{Kind: ClaimPanel, ID: "memory"})}
	if err := r.Register(accepted); err != nil {
		t.Fatalf("Register after a rejected attempt: %v", err)
	}
}

// TestRegisterClaimKindsAreIndependent 同一个 ID 在不同 claim 种类下不冲突。
func TestRegisterClaimKindsAreIndependent(t *testing.T) {
	r := NewRegistry()
	first := barePlugin{d: withClaims(desc("memory", DeploymentBuiltin), Claim{Kind: ClaimPanel, ID: "memory"})}
	if err := r.Register(first); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	second := barePlugin{d: withClaims(desc("memory-state", DeploymentBuiltin),
		Claim{Kind: ClaimStateNamespace, ID: "memory"})}
	if err := r.Register(second); err != nil {
		t.Fatalf("Register second: %v", err)
	}
	if got := len(r.Entries()); got != 2 {
		t.Fatalf("Entries = %d, want 2", got)
	}
}

// ---- Entries / Entry ----

func TestEntriesKeepsRegistrationOrder(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"alpha", "beta", "gamma"} {
		if err := r.Register(barePlugin{d: desc(id, DeploymentBuiltin)}); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}
	got := r.Entries()
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("Entries = %d entries, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].Descriptor.ID != id {
			t.Fatalf("Entries[%d] = %q, want %q", i, got[i].Descriptor.ID, id)
		}
	}
}

func TestEntriesReturnsCopies(t *testing.T) {
	r := NewRegistry()
	original := withClaims(desc("memory", DeploymentBuiltin), Claim{Kind: ClaimPanel, ID: "memory"})
	if err := r.Register(barePlugin{d: original}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// 注册后改调用方自己的描述符切片，注册表不应受影响。
	original.Claims[0].ID = "hacked"

	entries := r.Entries()
	entries[0].Descriptor.ID = "hacked"
	entries[0].Descriptor.Claims[0].ID = "hacked"
	entries[0].Descriptor.Contributions = nil
	entries[0].State = StateEnabled
	entries = append(entries, Entry{})

	if _, ok := r.Entry("hacked"); ok {
		t.Fatal("mutating a returned entry changed the registry")
	}
	got, ok := r.Entry("memory")
	if !ok {
		t.Fatal("plugin memory disappeared from the registry")
	}
	if got.Descriptor.Claims[0].ID != "memory" {
		t.Fatalf("claim id = %q, want %q", got.Descriptor.Claims[0].ID, "memory")
	}
	if got.State != StateRegistered {
		t.Fatalf("state = %q, want %q", got.State, StateRegistered)
	}
	if n := len(r.Entries()); n != 1 {
		t.Fatalf("Entries = %d, want 1", n)
	}
}

func TestEntryReturnsCopyAndNotFound(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(barePlugin{d: desc("memory", DeploymentBuiltin)}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := r.Entry("notes"); ok {
		t.Fatal("Entry returned an unregistered plugin")
	}

	got, ok := r.Entry("memory")
	if !ok {
		t.Fatal("Entry did not find the registered plugin")
	}
	got.Descriptor.Title = "hacked"
	again, _ := r.Entry("memory")
	if again.Descriptor.Title == "hacked" {
		t.Fatal("mutating the returned entry changed the registry")
	}
}

// ---- Enable / Disable ----

func TestEnableDisable(t *testing.T) {
	tests := []struct {
		name       string
		deployment Deployment
		prep       []Event // 登记后先执行的动作
		op         Event   // 待测动作
		wantState  State
		wantErr    string // 期望错误信息包含的子串
		// wantRecorded 表示这次失败是否应当被记入 Entry.Err（状态转 failed）。
		wantRecorded bool
	}{
		{
			name:       "registered builtin enables",
			deployment: DeploymentBuiltin,
			op:         EventEnable,
			wantState:  StateEnabled,
		},
		{
			name:       "enabled builtin disables",
			deployment: DeploymentBuiltin,
			prep:       []Event{EventEnable},
			op:         EventDisable,
			wantState:  StateDisabled,
		},
		{
			name:       "disabled builtin re-enables",
			deployment: DeploymentBuiltin,
			prep:       []Event{EventEnable, EventDisable},
			op:         EventEnable,
			wantState:  StateEnabled,
		},
		{
			name:       "enabling an enabled builtin errors",
			deployment: DeploymentBuiltin,
			prep:       []Event{EventEnable},
			op:         EventEnable,
			wantState:  StateEnabled,
			wantErr:    "already enabled",
		},
		{
			name:       "disabling a disabled builtin errors",
			deployment: DeploymentBuiltin,
			prep:       []Event{EventEnable, EventDisable},
			op:         EventDisable,
			wantState:  StateDisabled,
			wantErr:    "already disabled",
		},
		{
			name:         "disabling a registered builtin is an illegal transition",
			deployment:   DeploymentBuiltin,
			op:           EventDisable,
			wantState:    StateFailed,
			wantErr:      "not valid for a plugin in state",
			wantRecorded: true,
		},
		{
			name:       "process plugin cannot be enabled",
			deployment: DeploymentProcess,
			op:         EventEnable,
			wantState:  StateRegistered,
			wantErr:    "only enable or disable builtin plugins",
		},
		{
			name:       "process plugin cannot be disabled",
			deployment: DeploymentProcess,
			op:         EventDisable,
			wantState:  StateRegistered,
			wantErr:    "only enable or disable builtin plugins",
		},
		{
			name:       "browser plugin cannot be enabled",
			deployment: DeploymentBrowser,
			op:         EventEnable,
			wantState:  StateRegistered,
			wantErr:    "only enable or disable builtin plugins",
		},
		{
			name:       "browser plugin cannot be disabled",
			deployment: DeploymentBrowser,
			op:         EventDisable,
			wantState:  StateRegistered,
			wantErr:    "only enable or disable builtin plugins",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			if err := r.Register(barePlugin{d: desc("luna", tt.deployment)}); err != nil {
				t.Fatalf("Register: %v", err)
			}
			for _, e := range tt.prep {
				if err := applyEvent(r, "luna", e); err != nil {
					t.Fatalf("prep %s: %v", e, err)
				}
			}

			err := applyEvent(r, "luna", tt.op)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("%s: unexpected error %v", tt.op, err)
				}
			} else {
				if err == nil {
					t.Fatalf("%s: expected an error containing %q, got nil", tt.op, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("%s error %q does not contain %q", tt.op, err.Error(), tt.wantErr)
				}
			}

			got, ok := r.Entry("luna")
			if !ok {
				t.Fatal("plugin luna is missing from the registry")
			}
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q", got.State, tt.wantState)
			}
			if tt.wantRecorded && got.Err == nil {
				t.Fatal("a rejected transition must be recorded in Entry.Err")
			}
			if !tt.wantRecorded && got.Err != nil {
				t.Fatalf("Err = %v, want nil", got.Err)
			}
		})
	}
}

// TestEnableFailedBuiltin 确认 failed 状态不可再启用：非法迁移被拒后状态保持 failed，
// 且失败原因保留在 Entry.Err 里。
func TestEnableFailedBuiltin(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(barePlugin{d: desc("luna", DeploymentBuiltin)}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// registered --disable--> 非法：插件转入 failed 并记录原因。
	if err := r.Disable("luna"); err == nil {
		t.Fatal("Disable on a registered plugin: expected an error")
	}
	failed, _ := r.Entry("luna")
	if failed.State != StateFailed || failed.Err == nil {
		t.Fatalf("state = %q, Err = %v; want failed with a recorded error", failed.State, failed.Err)
	}

	// failed --enable--> 非法：不静默成功，状态仍是 failed。
	if err := r.Enable("luna"); err == nil {
		t.Fatal("Enable on a failed plugin: expected an error")
	} else if !strings.Contains(err.Error(), "not valid for a plugin in state") {
		t.Fatalf("Enable error %q does not report an illegal transition", err.Error())
	}
	got, _ := r.Entry("luna")
	if got.State != StateFailed || got.Err == nil {
		t.Fatalf("state = %q, Err = %v; want failed with a recorded error", got.State, got.Err)
	}
	if n := len(r.Enabled()); n != 0 {
		t.Fatalf("Enabled = %d, want 0", n)
	}
}

func TestEnableDisableUnknownPlugin(t *testing.T) {
	r := NewRegistry()
	for _, op := range []Event{EventEnable, EventDisable} {
		if err := applyEvent(r, "notes", op); err == nil {
			t.Fatalf("%s: expected an error for an unregistered plugin", op)
		} else if !strings.Contains(err.Error(), "is not registered") {
			t.Fatalf("%s error %q does not mention an unregistered plugin", op, err.Error())
		}
	}
}

// TestEnabledList 只返回 enabled 的插件，保持登记顺序，且仍是副本。
func TestEnabledList(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"alpha", "beta", "gamma"} {
		if err := r.Register(barePlugin{d: desc(id, DeploymentBuiltin)}); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}
	if got := r.Enabled(); len(got) != 0 {
		t.Fatalf("Enabled = %d before any enable, want 0", len(got))
	}
	for _, id := range []string{"alpha", "gamma"} {
		if err := r.Enable(id); err != nil {
			t.Fatalf("Enable %s: %v", id, err)
		}
	}

	got := r.Enabled()
	if len(got) != 2 {
		t.Fatalf("Enabled = %d entries, want 2", len(got))
	}
	if got[0].Descriptor.ID != "alpha" || got[1].Descriptor.ID != "gamma" {
		t.Fatalf("Enabled order = [%s %s], want [alpha gamma]", got[0].Descriptor.ID, got[1].Descriptor.ID)
	}
	if n := len(r.Entries()); n != 3 {
		t.Fatalf("Entries = %d, want 3", n)
	}

	got[0].State = StateDisabled
	again := r.Enabled()
	if len(again) != 2 || again[0].State != StateEnabled {
		t.Fatal("mutating the returned entries changed the registry")
	}
}

// ---- Grants ----

func TestGrants(t *testing.T) {
	tests := []struct {
		name string
		in   []PermissionKind
		want []PermissionKind
	}{
		{name: "no grant by default", in: nil, want: nil},
		{name: "single grant", in: []PermissionKind{PermissionStateWrite}, want: []PermissionKind{PermissionStateWrite}},
		{
			name: "duplicate grants are recorded once",
			in:   []PermissionKind{PermissionStateWrite, PermissionStateWrite},
			want: []PermissionKind{PermissionStateWrite},
		},
		{
			name: "grant order follows the constructor",
			in:   []PermissionKind{PermissionKind("state.read"), PermissionStateWrite},
			want: []PermissionKind{PermissionKind("state.read"), PermissionStateWrite},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry(tt.in...)
			got := r.Grants()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Grants() = %v, want %v", got, tt.want)
			}
			if len(got) > 0 {
				got[0] = PermissionKind("hacked")
				if again := r.Grants(); !reflect.DeepEqual(again, tt.want) {
					t.Fatalf("mutating the returned grants changed the registry: %v", again)
				}
			}
		})
	}
}

// TestRegistryIsSafeForConcurrentUse 只保证读写并发不会触发数据竞争，
// 具体状态由其他用例断言。
func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	r := NewRegistry(PermissionStateWrite)
	if err := r.Register(barePlugin{d: withPerms(desc("luna", DeploymentBuiltin),
		Permission{Kind: PermissionStateWrite})}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = r.Enable("luna")
			_ = r.Disable("luna")
		}
	}()
	for i := 0; i < 100; i++ {
		_ = r.Entries()
		_, _ = r.Entry("luna")
		_ = r.Enabled()
		_ = r.Grants()
	}
	<-done
}
