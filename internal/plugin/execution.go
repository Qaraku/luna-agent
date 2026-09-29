package plugin

import (
	"context"
	"fmt"
)

// ExecutionMode 是宿主已核准的模型命令权限，不是工具参数。
type ExecutionMode string

const (
	ExecutionSandbox    ExecutionMode = "sandbox"
	ExecutionFullAccess ExecutionMode = "full_access"
)

func (m ExecutionMode) Valid() bool { return m == ExecutionSandbox || m == ExecutionFullAccess }
func ExecutionFor(ctx context.Context) (ExecutionMode, error) {
	info, _ := Run(ctx)
	mode := info.ExecutionMode
	if mode == "" {
		mode = ExecutionSandbox
	}
	if !mode.Valid() {
		return "", fmt.Errorf("invalid execution mode")
	}
	return mode, nil
}
