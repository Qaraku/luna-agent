package plugin

import "context"

// RunInfo 标识一次运行。宿主在调用插件贡献的工具之前把它放进 ctx，插件凭此把状态
// 归因到正确的运行与会话。
//
// 这是插件能得到的全部运行身份。插件进程、代次这类部署形态自己的元数据不在这里：
// 那些是宿主给界面看的，不是插件用来记账的。
type RunInfo struct {
	RunID     string
	SessionID string
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
