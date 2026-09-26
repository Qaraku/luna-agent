package plugin

import "fmt"

// State 是插件在注册表里的生命周期状态。
type State string

const (
	// StateRegistered 是登记完成、尚未启用的状态。本轮只有 builtin 形态可以被
	// 启用或停用，process 与 browser 形态停留在登记态。
	StateRegistered State = "registered"
	StateEnabled    State = "enabled"
	StateDisabled   State = "disabled"
	StateFailed     State = "failed"
)

// Event 是驱动状态迁移的事件。
type Event string

const (
	EventEnable  Event = "enable"
	EventDisable Event = "disable"
	EventFail    Event = "fail"
)

// Transition 是纯函数：返回从 from 接受 e 后的新状态；非法迁移返回错误。
//
// 合法迁移只有：
//
//	registered --enable--> enabled
//	disabled   --enable--> enabled
//	enabled    --disable--> disabled
//	任意状态   --fail--> failed
//
// 其余（含 enabled--enable、failed--enable、failed--disable、registered--disable、
// 未知事件）非法。非法时返回原状态与说明原因的错误，调用方据此把插件标记为 failed。
func Transition(from State, e Event) (State, error) {
	if e == EventFail {
		return StateFailed, nil
	}
	switch e {
	case EventEnable:
		if from == StateRegistered || from == StateDisabled {
			return StateEnabled, nil
		}
	case EventDisable:
		if from == StateEnabled {
			return StateDisabled, nil
		}
	}
	return from, fmt.Errorf("event %q is not valid for a plugin in state %q", e, from)
}
