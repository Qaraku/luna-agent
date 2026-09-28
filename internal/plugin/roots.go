package plugin

import "context"

// rootsKey 是 ctx 里“本次运行可以工作的目录集合”的键。
type rootsKey struct{}

// WithRoots 把一次运行可以工作的目录集合放进 ctx，供宿主调用的任意模型可见工具读取。
//
// 它同时是两件事的边界：文件工具的可读范围，以及命令执行的工作目录边界。宿主在调用
// 任何模型可见工具之前设置它，插件凭它判断一次调用是否落在允许的范围内。目录按顺序
// 尝试，第一个包含目标路径的目录胜出。
//
// 空集合照原样放进 ctx，不在这里替换成某个默认目录：默认目录由宿主决定，在这一层补
// 一次会让“范围是谁定的”变得看不出来。
func WithRoots(ctx context.Context, roots []string) context.Context {
	return context.WithValue(ctx, rootsKey{}, roots)
}

// Roots 读回本次运行可以工作的目录集合。
//
// ctx 里没有设置过时返回 nil，这与设置成空集合同义——两者都表示“这次运行没有可工作的
// 目录”。调用方应当据此拒绝需要工作目录的调用，而不是自己猜一个默认目录；需要“未绑定
// 会话回退到单一 root”这种行为时，回退应当在设置之前由宿主完成。
func Roots(ctx context.Context) []string {
	v, _ := ctx.Value(rootsKey{}).([]string)
	return v
}
