package plugin

import (
	"context"
	"github.com/Qaraku/luna-agent/internal/runconfig"
)

// RunInfo 标识一次运行。宿主在调用插件贡献的工具之前把它放进 ctx，插件凭此把状态
// 归因到正确的运行与会话。
//
// 这是插件能得到的全部运行身份。插件进程、代次这类部署形态自己的元数据不在这里：
// 那些是宿主给界面看的，不是插件用来记账的。
type RunInfo struct {
	RunID     string
	SessionID string
	// Selection 与 Resources 是本轮选择及已冻结的资源名字，只能缩小可用集合。
	Selection *runconfig.Selection
	Resources map[string][]string
	// ExecutionMode 来自宿主授权快照，不接受模型参数或未经核准的持久化字段。
	ExecutionMode ExecutionMode
	// Permissions 与 Approve 只来自宿主运行快照，不从工具参数或会话文件直接恢复。
	Permissions *AccessPolicy
	// AutomaticWriteDirs 是宿主在准入时读取的自动写入范围，不由模型参数设置。
	AutomaticWriteDirs []string
	WriteScopeError    string
	Approve            ApprovalFunc
}

type runInfoKey struct{}

// WithRun 把运行标识放进 ctx。
func WithRun(ctx context.Context, info RunInfo) context.Context {
	return context.WithValue(ctx, runInfoKey{}, info)
}

// Run 读回运行标识。第二个返回值说明宿主是否设置过它：没有设置时返回零值，
// 调用方应当按“没有归因信息”处理，不要编造一个标识。
func Run(ctx context.Context) (RunInfo, bool) {
	info, ok := ctx.Value(runInfoKey{}).(RunInfo)
	return info, ok
}

// RunResourceProvider 由能力提供其当前可用资源名，宿主冻结为本轮快照。
// 名字的业务含义属于能力，宿主只按选择做交集。
type RunResourceProvider interface {
	RunResources(context.Context) ([]string, error)
}

func ResourceSelected(ctx context.Context, owner, name string) bool {
	info, ok := Run(ctx)
	if !ok {
		return true
	}
	if names, exists := info.Resources[owner]; exists {
		for _, selected := range names {
			if name == selected {
				return true
			}
		}
		return false
	}
	return info.Selection.AllowsResource(owner, name)
}

// SetupProvider 从能力自己的配置目录解析一次不可变运行选择。宿主只负责绑定会话，
// 不解释目录内容；配置目录的读写由该能力已声明的路由提供。
type SetupProvider interface {
	ResolveSetup(id, revision string) (*runconfig.Selection, error)
}
