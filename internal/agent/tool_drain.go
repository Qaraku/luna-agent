package agent

import (
	"context"
	"sync"
)

type toolDrainKey struct{}

// toolDrain 在关闭准入后等待已开始的调用完全退出，避免取消事件抢在工具终态之前。
type toolDrain struct {
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

func trackTool(ctx context.Context) (func(), error) {
	drain, _ := ctx.Value(toolDrainKey{}).(*toolDrain)
	if drain == nil {
		return func() {}, nil
	}
	drain.mu.Lock()
	defer drain.mu.Unlock()
	if drain.closed {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	drain.active.Add(1)
	return drain.active.Done, nil
}
func (d *toolDrain) closeAndWait() { d.mu.Lock(); d.closed = true; d.mu.Unlock(); d.active.Wait() }
