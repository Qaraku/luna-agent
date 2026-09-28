// Package web 是内置的联网能力：模型可以把一个公开的 http(s) 地址取回来，读它的正文
// 文本。本轮只有这一个方向——抓取；搜索需要外部后端，还没有。
//
// 它与其它能力走同一套声明面：只贡献一个工具 luna_web_fetch，不认领任何路由、面板或
// 状态，并显式申请 plugin.PermissionNetworkFetch。宿主没有显式授权这项权限时，注册表
// 会在注册阶段拒绝它——内置不是特权，放开出网必须由宿主明确授权。
//
// 出网是这项能力最难收窄的边界，因此策略写在能力自己里面，分两层执行：请求前解析主机名
// 判地址，拨号时用 net.Dialer.Control 对解析后的地址再判一次。两层都只放行公开地址，
// 拒绝 loopback、RFC1918 私有段、link-local、IPv6 ULA、未指定、组播，以及把别的地址装在
// 里面的 6to4 与 NAT64 前缀，v4 与 v6 都管。
package web

import "github.com/Qaraku/luna-agent/internal/plugin"

const (
	// PluginID 是注册表里的插件 id。
	PluginID = "web"
	// PluginTitle 是插件对人类可读的标题。
	PluginTitle = "联网"
	// FetchToolName 是模型可见的抓取工具名。
	FetchToolName = "luna_web_fetch"
)

// Plugin 是联网能力：一个模型可见的工具，没有别的。
type Plugin struct{ tool *FetchTool }

// New 建一个联网能力。它不需要参数：地址策略、上限与代理判定都写在这项能力自己里面，
// 工具也不持有任何跨调用共享的状态。
func New() *Plugin { return &Plugin{tool: NewFetchTool()} }

// Descriptor 声明联网能力暴露什么：一个工具，一项权限申请，没有任何 claim。
func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionTool, ID: FetchToolName},
		},
		Permissions: []plugin.Permission{
			// 出网是受权限权威管理的可授权项：没有这条授权，注册会被拒。
			{Kind: plugin.PermissionNetworkFetch},
		},
	}
}

// Descriptor 实现 plugin.Plugin。
func (p *Plugin) Descriptor() plugin.Descriptor { return Descriptor() }

// Tools 返回模型可见的面：只有 luna_web_fetch 一个工具。
func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{p.tool} }

// 编译期断言：描述符声明的可选接口必须真的实现，漂移会在这里编译不过。
var (
	_ plugin.Plugin       = (*Plugin)(nil)
	_ plugin.ToolProvider = (*Plugin)(nil)
)
