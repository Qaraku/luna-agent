// Package filewrite 是「写文件」能力：一个模型可见工具，在用户显式允许写入的
// 目录里新建或整文件替换一个文本文件，并把改动以 diff 的形式交回模型。
//
// 边界不由它自己实现：路径交给 internal/fileread 解释（ResolveWriteInRoots，
// 根是本次运行的工作目录），再收窄到用户那张允许清单（settings 的 write.dir 段）。
// 一次调用因此要同时满足三件事：路径落在本次运行的某个工作目录里、那个目录也在用户
// 允许写入的目录里、内容是这个工具能原样读回来的文本。任何一条不成立就是拒绝本次
// 调用，磁盘上一个字节都不动。
//
// 真正落盘的写入走 internal/atomicfile，读者看不到半写状态。
//
// 内建是一种部署选择，不是特权：它声明的是任何能力都要声明的那份东西——工具贡献与
// fs.write 权限；注册表没有授予这条权限时，这个能力根本注册不上。
package filewrite

import "github.com/Qaraku/luna-agent/internal/plugin"

const (
	// PluginID is the registry id of the File write capability.
	PluginID = "filewrite"
	// PluginTitle is the capability's human-readable title.
	PluginTitle = "File write"
)

// Plugin is the File write capability: exactly one model-visible tool, no
// context contribution, no route, no panel and no state of its own. What it
// knows at construction time is where the user's settings file is; what it
// knows at call time is the run it is being called in.
type Plugin struct{ tool *WriteTool }

// New binds the capability to the settings file the user's own choices live in.
// settingsPath is a host path, resolved by the composition root; an empty one is
// not an error here, because it means the same thing the file's absence means —
// no directory has been allowed, so nothing may be written — and that answer
// comes from the tool itself, in the words a model can act on.
func New(settingsPath string) *Plugin { return &Plugin{tool: NewWriteTool(settingsPath)} }

// Descriptor declares exactly what this capability exposes. The registry checks
// both directions, so it has to stay in step with Tools: a declared tool that is
// never exposed fails registration.
//
// It is a package-level function as well as a method because the composition
// root needs the descriptor — and therefore the permission it asks for — before
// it builds the plugin.
func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionTool, ID: WriteToolName},
		},
		// The one thing this capability needs from the Kernel that it cannot do
		// itself: permission to change files on the user's machine. It keeps no
		// state of its own (no state namespace, hence no state.write), serves no
		// route and no panel (hence no route-prefix and no panel id), and the
		// path boundary is internal/fileread's, not a second implementation
		// living here.
		Permissions: []plugin.Permission{
			{Kind: plugin.PermissionFilesystemWrite},
		},
	}
}

// Descriptor implements plugin.Plugin.
func (p *Plugin) Descriptor() plugin.Descriptor { return Descriptor() }

// Tools returns the model-visible surface: exactly luna_write_file.
func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{p.tool} }

// The one provider interface the descriptor declares.
var _ plugin.ToolProvider = (*Plugin)(nil)
