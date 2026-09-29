package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// resolveHostDir 只在宿主已经明确授予 Full access 后使用；此时目录不再限于工作区。
func resolveHostDir(roots []string, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return defaultDir(roots)
	}
	candidates := []string{cwd}
	if !filepath.IsAbs(cwd) {
		candidates = nil
		for _, root := range roots {
			candidates = append(candidates, filepath.Join(root, cwd))
		}
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err == nil && info.IsDir() {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("cwd does not name an existing host directory")
}
func hostCommandEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	home, _ := os.UserHomeDir()
	return []string{"PATH=" + path, "HOME=" + home, "TMPDIR=" + os.TempDir()}
}

// runNative 不使用 Bubblewrap，也不是隔离失败的后备。调用方必须已经验证 Full access。
// 它清理本次进程组；已拥有宿主权限的程序可以故意脱离进程组，不能承诺沙箱式清理。
func (t *RunTool) runNative(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = hostCommandEnv()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = killGrace
	capture := newOutputCapture(MaxOutputBytes)
	cmd.Stdout = capture.writer(stdoutStream)
	cmd.Stderr = capture.writer(stderrStream)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return "", plugin.Unavailable(fmt.Errorf("start full-access terminal: %w", err))
	}
	pgid := cmd.Process.Pid
	// 父进程已退出后只清理本组，不再向已回收的正 PID 发信号。
	defer syscall.Kill(-pgid, syscall.SIGKILL)
	stopNative := func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		// os.Process 记得 Wait 是否已完成，避免向已回收的正 PID 发信号。
		_ = cmd.Process.Kill()
	}
	waited := make(chan int, 1)
	go func() { _ = cmd.Wait(); waited <- exitCode(cmd) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	finished, timedOut := false, false
	code := -1
	waitAfterKill := func() {
		select {
		case code = <-waited:
		case <-time.After(killGrace + 250*time.Millisecond):
		}
	}
	select {
	case code = <-waited:
		finished = true
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
	}
	if stopped := ctx.Err(); stopped != nil {
		if !finished {
			stopNative()
			waitAfterKill()
		}
		return "", stopped
	}
	if timedOut {
		stopNative()
		waitAfterKill()
	}
	return "execution mode: full_access (host user permissions; no sandbox)\n" + renderResult(code, dir, time.Since(started), timeout, timedOut, capture), nil
}
