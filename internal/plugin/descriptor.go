// Package plugin 定义 Luna 的插件能力模型：描述符、能力原语、生命周期状态机
// 与注册表。
//
// 本轮只落地类型与规则：注册表校验描述符与插件实现的一致性、claim（能力占用
// 声明）冲突与权限授权，但不做装配——不启动子进程、不挂载路由、不渲染上下文。
// 这些类型是后续 Kernel 的最小底座，先固定契约，再由装配层使用。
package plugin

import "context"

// Deployment 是插件的部署形态，说明能力由谁承载、进程边界在哪里。
type Deployment string

const (
	// DeploymentBuiltin 表示插件编译进主进程。
	DeploymentBuiltin Deployment = "builtin"
	// DeploymentProcess 表示插件运行在独立子进程。
	DeploymentProcess Deployment = "process"
	// DeploymentBrowser 表示插件是浏览器侧模块。
	DeploymentBrowser Deployment = "browser"
)

// ContributionKind 是插件可以贡献的能力种类。
type ContributionKind string

const (
	ContributionTool    ContributionKind = "tool"
	ContributionContext ContributionKind = "context"
	ContributionRoute   ContributionKind = "route"
	ContributionPanel   ContributionKind = "panel"
)

// DefaultContributionBudget 是 Contribution.BudgetBytes 为 0 时 Kernel 采用的
// 字节上限。上限只由 Kernel 决定，插件声明的是自己期望的值。
const DefaultContributionBudget = 4 * 1024

// Contribution 声明插件贡献的一个能力项。BudgetBytes 是 Kernel 为该贡献设定的
// 字节上限；0 表示使用 Kernel 的默认上限（DefaultContributionBudget）。
//
// 声明不等于实现：注册表会校验声明与插件实现的接口一致。ID 的含义随 Kind 变化
// ——tool 用工具名，context 用上下文 ID，route 用路径，panel 用面板 ID。
type Contribution struct {
	Kind        ContributionKind
	ID          string
	BudgetBytes int
}

// ClaimKind 是插件向 Kernel 申请独占占用的资源种类。
type ClaimKind string

const (
	// ClaimRoutePrefix 是路由前缀，形如 /api/memory。
	ClaimRoutePrefix ClaimKind = "route-prefix"
	// ClaimPanel 是面板 id。
	ClaimPanel ClaimKind = "panel"
	// ClaimStateNamespace 是状态目录名，不含分隔符。
	ClaimStateNamespace ClaimKind = "state-namespace"
)

// Claim 是一份独占占用声明：两个插件不能占用同一个 (Kind, ID)。
type Claim struct {
	Kind ClaimKind
	ID   string
}

// PermissionKind 是受权限权威管理的权限种类，目前只有一种可授权项。
type PermissionKind string

const PermissionStateWrite PermissionKind = "state.write"

// Permission 是插件对某个受控能力的申请。是否可授予由注册表持有的授权表决定；
// Detail 预留给以后的细粒度授权，目前只接受空串。
type Permission struct {
	Kind   PermissionKind
	Detail string
}

// Descriptor 是插件对自身的完整声明，也是注册表校验的唯一输入。
type Descriptor struct {
	ID            string
	Title         string
	Deployment    Deployment
	Contributions []Contribution
	Claims        []Claim
	Permissions   []Permission
}

// Plugin 是所有插件（不区分部署形态）都必须实现的接口。
type Plugin interface{ Descriptor() Descriptor }

// 下面四个是可选接口：插件实现了哪个就应当在自己的 Contributions 里声明对应的项，
// 注册表会校验两者一致。
type ToolProvider interface{ Tools() []Tool }
type ContextProvider interface {
	Contexts(ctx context.Context) ([]ContextBlock, error)
}
type RouteProvider interface{ Routes() []Route }
type PanelProvider interface{ Panels() []Panel }
