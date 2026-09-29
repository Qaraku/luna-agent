package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// ReadOnlyMount 只由可信宿主提供，不属于 luna_run 的模型参数。
type ReadOnlyMount struct{ Source, Target string }

func ReadonlyMount(source, target string) ReadOnlyMount { return ReadOnlyMount{source, target} }

// IsolatedRequest 是已经核准范围后的宿主执行契约，始终隔离，不支持 Full access 回退。
type IsolatedRequest struct {
	Command, Dir          string
	ReadRoots, WriteRoots []string
	Readonly              []ReadOnlyMount
	Stdin                 []byte
	Network               bool
	Timeout               time.Duration
}
type IsolatedResult struct {
	Stdout, Stderr      string
	ExitCode            int
	Truncated, TimedOut bool
}

func ExecuteIsolated(ctx context.Context, in IsolatedRequest) (IsolatedResult, error) {
	result := IsolatedResult{ExitCode: -1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if in.Command == "" || len(in.Command) > 16384 || len(in.Stdin) > 128*1024 || len(in.Readonly) > 8 {
		return result, fmt.Errorf("host command exceeds argument/resource limits")
	}
	if in.Timeout <= 0 {
		in.Timeout = DefaultTimeout
	}
	if in.Timeout > MaxTimeout {
		return result, fmt.Errorf("host command timeout exceeds limit")
	}
	if in.Dir == "" {
		in.Dir = "/tmp/work"
	}
	sandbox, err := prepareSandboxWithAccess(ctx, sandboxExecutable, in.ReadRoots, in.Dir, in.Command, sandboxAccess{Read: len(in.ReadRoots) > 0, WriteRoots: in.WriteRoots, Network: in.Network, Readonly: in.Readonly})
	if err != nil {
		return result, err
	}
	defer sandbox.close()
	cmd := sandbox.cmd
	cmd.Stdin = bytes.NewReader(in.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	capture := newOutputCapture(MaxOutputBytes)
	cmd.Stdout = capture.writer(stdoutStream)
	cmd.Stderr = capture.writer(stderrStream)
	if err = cmd.Start(); err != nil {
		return result, plugin.Unavailable(fmt.Errorf("start isolated host command: %w", err))
	}
	sandbox.closeParentFiles()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(in.Timeout)
	defer timer.Stop()
	finished := false
	select {
	case <-done:
		finished = true
	case <-ctx.Done():
	case <-timer.C:
		result.TimedOut = true
	}
	if !finished {
		killGroup(cmd.Process.Pid)
		waitOrGiveUp(done)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !sandbox.ready() {
		return result, plugin.Unavailable(errors.New("host command isolation did not start; no unsandboxed fallback was used"))
	}
	result.ExitCode = exitCode(cmd)
	capture.mu.Lock()
	result.Stdout = string(capture.kept[stdoutStream])
	result.Stderr = string(capture.kept[stderrStream])
	result.Truncated = capture.dropped[stdoutStream] > 0 || capture.dropped[stderrStream] > 0
	capture.mu.Unlock()
	return result, nil
}
