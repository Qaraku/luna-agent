package plugin

import (
	"errors"
	"fmt"
)

// ErrUnavailable 表示插件无法服务本次调用，而原因不在模型的调用内容上：它自己的状态
// 文件写不进去、依赖的服务起不来，都属于这一类。
//
// 宿主按基础设施故障处理它：结束整轮，而不是回给模型一句“工具拒绝了这次调用”。
// 参数、路径、大小这类**由模型输入导致**的拒绝不需要这个标记——那些是调用结果，
// 模型可以自己纠正。区分两者的理由和插件工具完全一样：模型无法对“存储坏了”做出
// 任何有用的事，而用户需要知道这次运行真的失败了。
var ErrUnavailable = errors.New("the capability cannot serve this call")

// Unavailable 把插件内部的一次失败标记成基础设施故障，同时保留原因文本。
// 两个原因链都保留，因此 errors.Is 既能配到 ErrUnavailable，也能配到 err 本身。
func Unavailable(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// IsUnavailable 报告 err 是否被标记为插件侧的基础设施故障。
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }
