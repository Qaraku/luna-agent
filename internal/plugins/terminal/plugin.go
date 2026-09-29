// Package terminal 是内置的终端能力：模型可以在本次运行的工作目录里执行一条命令，
// 并拿回它的退出码、耗时、实际工作目录与两路输出。
//
// 它与其它能力走同一套声明面：贡献 luna_run 和当前执行权限上下文，不认领路由、面板或状态，
// 并显式申请命令执行、文件写入和联网权限。宿主未授予其中任一项时，注册表会在
// 注册阶段拒绝它——内置不是特权，放开命令执行必须由宿主明确授权。
//
// 能力自己不做路径解释：工具参数里的 cwd 交给 internal/fileread 的边界检查，与文件
// 工具是同一套实现（同一处规范化、同一处符号链接解析、同一处包含判定）。
package terminal

import (
	"context"
	"fmt"
	"github.com/Qaraku/luna-agent/internal/plugin"
)

const (
	// PluginID 是注册表里的插件 id。
	PluginID = "terminal"
	// PluginTitle 是插件对人类可读的标题。
	PluginTitle = "终端"
	// RunToolName 是模型可见的命令执行工具名。
	RunToolName = "luna_run"
)

// Plugin 是终端能力：执行工具和与它一致的授权模式说明。
type Plugin struct{ tool *RunTool }

// New 建一个终端能力。它不需要参数：工作目录来自每次运行的 ctx，工具自己也不持有
// 任何跨调用共享的状态。
func New() *Plugin { return &Plugin{tool: NewRunTool()} }

// Descriptor 声明执行工具、权限上下文和进程权限申请。
func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionTool, ID: RunToolName},
			{Kind: plugin.ContributionContext, ID: "execution", BudgetBytes: 1024},
		},
		Permissions: []plugin.Permission{
			// 执行命令是受权限权威管理的可授权项：没有这条授权，注册会被拒。
			{Kind: plugin.PermissionProcessExec},
			{Kind: plugin.PermissionFilesystemWrite},
			{Kind: plugin.PermissionNetworkFetch},
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

// Contexts 的权限事实来自内核授权快照，而不是模型声明的参数或项目文件。
func (p *Plugin) Contexts(ctx context.Context) ([]plugin.ContextBlock, error) {
	mode, err := plugin.ExecutionFor(ctx)
	if err != nil {
		return nil, plugin.Unavailable(err)
	}
	policy := plugin.AccessPolicyFor(ctx)
	text := fmt.Sprintf("Terminal mode: sandbox. Effective policy: read=%s, write=%s, network=%s, exec=%s. Commands default to read-only project mounts and no network. Request write=true with relative write_dirs (default cwd) for writable mounts; request network=true for public HTTP(S)/CONNECT via the isolated proxy. Host/private addresses, direct sockets and UDP remain inaccessible. read=false runs in private scratch with no project mounts and requires cwd to be omitted. Requests cannot override deny; ask pauses for a one-time user decision before execution. Private scratch stays writable. A writable project mount requires read permission too.", policy.Read, policy.Write, policy.Network, policy.Exec)
	if mode == plugin.ExecutionFullAccess {
		text = "Terminal execution mode: full_access. The user explicitly authorized host-current-user file and network access for this session in this service process. Commands run without Bubblewrap; cwd may be absolute or outside the workspace. This can modify/delete host files, reach credentials and use host networking. It does not grant root privileges. Time/output limits remain; process-group cleanup cannot contain deliberately detached processes. Do not claim this mode is a sandbox."
	}
	return []plugin.ContextBlock{{ID: "execution", Kind: plugin.ContextReference, Text: text}}, nil
}

var _ plugin.ContextProvider = (*Plugin)(nil)
