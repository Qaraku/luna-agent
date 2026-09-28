// Package terminal 是内置的终端能力：模型可以在本次运行的工作目录里执行一条命令，
// 并拿回它的退出码、耗时、实际工作目录与两路输出。
//
// 它与其它能力走同一套声明面：只贡献一个工具 luna_run，不认领任何路由、面板或状态，
// 并显式申请 plugin.PermissionProcessExec。宿主没有显式授权这项权限时，注册表会在
// 注册阶段拒绝它——内置不是特权，放开命令执行必须由宿主明确授权。
//
// 能力自己不做路径解释：工具参数里的 cwd 交给 internal/fileread 的边界检查，与文件
// 工具是同一套实现（同一处规范化、同一处符号链接解析、同一处包含判定）。
package terminal

import "github.com/Qaraku/luna-agent/internal/plugin"

const (
	// PluginID 是注册表里的插件 id。
	PluginID = "terminal"
	// PluginTitle 是插件对人类可读的标题。
	PluginTitle = "终端"
	// RunToolName 是模型可见的命令执行工具名。
	RunToolName = "luna_run"
)

// Plugin 是终端能力：一个模型可见的工具，没有别的。
type Plugin struct{ tool *RunTool }

// New 建一个终端能力。它不需要参数：工作目录来自每次运行的 ctx，工具自己也不持有
// 任何跨调用共享的状态。
func New() *Plugin { return &Plugin{tool: NewRunTool()} }

// Descriptor 声明终端能力暴露什么：一个工具，一项权限申请，没有任何 claim。
func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionTool, ID: RunToolName},
		},
		Permissions: []plugin.Permission{
			// 执行命令是受权限权威管理的可授权项：没有这条授权，注册会被拒。
			{Kind: plugin.PermissionProcessExec},
		},
	}
}

// Descriptor 实现 plugin.Plugin。
func (p *Plugin) Descriptor() plugin.Descriptor { return Descriptor() }

// Tools 返回模型可见的面：只有 luna_run 一个工具。
func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{p.tool} }

// 编译期断言：描述符声明的可选接口必须真的实现，漂移会在这里编译不过。
var (
	_ plugin.Plugin       = (*Plugin)(nil)
	_ plugin.ToolProvider = (*Plugin)(nil)
)
