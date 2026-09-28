package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
)

// kernelRefusalPrefix 是内核给能力工具拒绝时用的前缀（internal/agent 的
// refuseCapability），AGENTS.md 把它写死。工具本身返回的是不带前缀的错误——前缀属于
// 内核，在这里再加一次，模型会读到两遍。这些测试因此断言两件事：拒绝是一条错误（因此
// 模型可纠正、整轮继续），以及内核加上前缀后的模型可见文本里确实带着这个前缀。
const kernelRefusalPrefix = "the tool refused this call: "

// runCtx 造一个带着本次运行工作目录的 ctx，与宿主调用工具时放进去的东西一致。
func runCtx(roots ...string) context.Context {
	return plugin.WithRoots(context.Background(), roots)
}

// args 把一次调用的参数编码成模型会送过来的 JSON。
func args(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// refused 断言一次调用被拒绝：返回错误、带内核对拒绝的语义（不是基础设施故障），并且
// 内核加上前缀之后的模型可见文本里带着每一个期望的子串。
func refused(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the tool to refuse this call")
	}
	if plugin.IsUnavailable(err) {
		t.Fatalf("a call-level refusal must not be marked as an infrastructure failure: %v", err)
	}
	if strings.Contains(err.Error(), kernelRefusalPrefix) {
		t.Fatalf("the tool prefixed its own refusal; the prefix belongs to the kernel: %q", err)
	}
	visible := kernelRefusalPrefix + err.Error()
	for _, want := range wants {
		if !strings.Contains(visible, want) {
			t.Fatalf("model-visible refusal %q does not name %q", visible, want)
		}
	}
}

// resolved 是 fileread 会把一个路径解析成的样子（符号链接解析之后），用来和工具报告的
// 实际工作目录比较：临时目录本身可能是符号链接。
func resolved(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return got
}

// The ordinary case: a command runs, its standard output comes back labelled, and the
// result names the directory it actually ran in.
func TestRunExecutesACommandAndReportsItsExitCodeOutputAndCwd(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	got, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{
		"command": `printf hi; printf boom >&2`,
	}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	for _, want := range []string{"exit code 0", "stdout:", "stderr:", "hi", "boom", root} {
		if !strings.Contains(got, want) {
			t.Fatalf("result %q does not carry %q", got, want)
		}
	}
}

// A non-zero exit is the answer to a call that worked, not a failure of it: the model
// must be able to read the exit code and decide what to do.
func TestRunReportsANonZeroExitCodeAsAResult(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	got, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{"command": "exit 3"}))
	if err != nil {
		t.Fatalf("a non-zero exit must be a result, not an error: %v", err)
	}
	if !strings.Contains(got, "exit code 3") {
		t.Fatalf("result %q does not report the exit code", got)
	}
}

// A cwd that leaves the run's working directory is refused, through the same boundary
// check the file tools use. A `..` climb and an absolute path are the two shapes of it.
func TestRunRefusesACwdOutsideTheWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	// A real sibling directory next to the root, so the escape is a real one.
	outside := filepath.Join(filepath.Dir(root), "elsewhere")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"a .. that climbs above the root", "../elsewhere", fileread.ErrPathEscape.Error()},
		{"an absolute path", outside, fileread.ErrPathAbsolute.Error()},
		{"a path that does not exist", "nope/deeper", fileread.ErrNotFound.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{
				"command": "printf hi",
				"cwd":     tt.cwd,
			}))
			refused(t, err, tt.want)
		})
	}
}

// A cwd inside the working directory selects where the command runs.
func TestRunUsesACwdInsideTheWorkingDirectory(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{
		"command": "pwd",
		"cwd":     "sub",
	}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if want := resolved(t, sub); !strings.Contains(got, want) {
		t.Fatalf("result %q does not report the cwd %q", got, want)
	}
}

// A command that outlives its timeout is stopped, and the stop reaches the whole process
// group: a subshell that kept writing proves nothing outlived the kill.
func TestRunStopsACommandThatTimesOutAndKillsItsProcessGroup(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	tool, pidFile := trackedSandboxTool(t)
	got, err := tool.Invoke(runCtx(root), args(t, map[string]any{"command": "setsid sh -c 'sleep 30' & wait", "timeout_s": 1}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stopped", "timeout", "process group"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	// 沙箱里的 $$ 是命名空间 PID，不能拿它给宿主发信号。跟踪的是包装器的宿主 PID。
	waitUntilGone(t, readPid(t, pidFile))
}

// Output over the cap is stated, with what was shown and what was withheld: a result
// that quietly carried only the beginning would describe a smaller run than the one
// that happened.
func TestRunStatesWhenTheOutputCapIsReached(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	const total = 40000
	got, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{
		"command": fmt.Sprintf("head -c %d /dev/zero | tr '\\0' 'a'", total),
	}))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("result does not say the output was truncated: %q", got)
	}
	for _, want := range []string{
		fmt.Sprintf("first %d bytes are shown", MaxOutputBytes),
		fmt.Sprintf("%d more bytes were not given", total-MaxOutputBytes),
		fmt.Sprintf("%d-byte total output cap", MaxOutputBytes),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("result %q does not state %q", got, want)
		}
	}
}

func TestRunRefusesAnEmptyCommand(t *testing.T) {
	root := t.TempDir()
	for _, command := range []string{"", "   ", "\n"} {
		_, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{"command": command}))
		refused(t, err, "command is required")
	}
}

func TestRunRefusesWhenTheRunHasNoWorkingDirectory(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want []string
	}{
		// A run with no directory is a session that names no workspace, and the refusal
		// has to point at the control that fixes it rather than only saying "nowhere".
		{"no roots were ever set", context.Background(), []string{"no directory", "工作区"}},
		{"the roots are empty", runCtx(), []string{"no directory", "工作区"}},
		{"every root is missing", runCtx(filepath.Join(t.TempDir(), "gone")), []string{"working directories exists"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRunTool().Invoke(tt.ctx, args(t, map[string]any{"command": "printf hi"}))
			for _, want := range tt.want {
				refused(t, err, want)
			}
		})
	}
}

// A malformed call is refused before any process is reached.
func TestRunRefusesArgumentsThatAreNotOneJsonObject(t *testing.T) {
	root := t.TempDir()
	for _, arguments := range []string{
		`not json`,
		`{"command":"a"}{"command":"b"}`,
		`{"command":"a","unknown":1}`,
		``,
	} {
		_, err := NewRunTool().Invoke(runCtx(root), arguments)
		refused(t, err, "JSON object")
	}
}

// A timeout outside the accepted range is refused rather than silently clamped.
func TestRunRefusesATimeoutOutsideTheAcceptedRange(t *testing.T) {
	root := t.TempDir()
	for _, secs := range []int{-1, int(MaxTimeout/time.Second) + 1} {
		_, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{
			"command":   "printf hi",
			"timeout_s": secs,
		}))
		refused(t, err, "timeout_s must be between")
	}
}

// A cancelled run hands the context's own error back to the kernel, which treats it as
// the end of the round rather than as this call's refusal.
func TestRunReturnsTheContextErrorWhenTheRunIsCancelled(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	tool, pidFile := trackedSandboxTool(t)
	ctx, cancel := context.WithCancel(runCtx(root))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tool.Invoke(ctx, args(t, map[string]any{"command": "setsid sh -c 'sleep 30' & wait", "timeout_s": 60}))
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("isolated process did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command did not return")
	}
	waitUntilGone(t, readPid(t, pidFile))
}

// The tool's public shape: the name the model calls, the one required parameter, no
// undeclared parameters, and a description that says what the model needs to know.
func TestRunToolSchemaAndDescriptionShape(t *testing.T) {
	tool := NewRunTool()
	if tool.Name() != RunToolName || RunToolName != "luna_run" {
		t.Fatalf("tool name = %q", tool.Name())
	}

	encoded, err := json.Marshal(tool.Schema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["type"] != "object" || raw["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %s", encoded)
	}
	required, ok := raw["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "command" {
		t.Fatalf("required = %v, want exactly [command]", raw["required"])
	}
	properties, ok := raw["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", raw["properties"])
	}
	for _, name := range []string{"command", "cwd", "timeout_s"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("schema has no %q parameter: %s", name, encoded)
		}
	}
	if len(properties) != 3 {
		t.Fatalf("the tool exposes an undeclared parameter: %s", encoded)
	}

	text := tool.Description()
	for _, want := range []string{
		"sh -c",
		"exit code",
		"stdout",
		"stderr",
		"timeout_s",
		"directories this session works in",
		"minimal environment",
		"cap",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the description must say %q: %q", want, text)
		}
	}
}

// The descriptor is the capability's whole declaration: one tool, the exec permission,
// and no claim on anything the capability does not provide.
func TestTheDescriptorDeclaresOneToolAndTheExecPermission(t *testing.T) {
	d := Descriptor()
	if d.ID != PluginID || PluginID != "terminal" {
		t.Fatalf("id = %q", d.ID)
	}
	if d.Title != PluginTitle || PluginTitle != "终端" {
		t.Fatalf("title = %q", d.Title)
	}
	if d.Deployment != plugin.DeploymentBuiltin {
		t.Fatalf("deployment = %q", d.Deployment)
	}
	if len(d.Claims) != 0 {
		t.Fatalf("the terminal capability must claim nothing: %+v", d.Claims)
	}
	if len(d.Contributions) != 1 || d.Contributions[0].Kind != plugin.ContributionTool || d.Contributions[0].ID != RunToolName {
		t.Fatalf("contributions = %+v", d.Contributions)
	}
	if len(d.Permissions) != 1 || d.Permissions[0].Kind != plugin.PermissionProcessExec || d.Permissions[0].Detail != "" {
		t.Fatalf("permissions = %+v", d.Permissions)
	}
}

// The exposed tools are exactly the tools the descriptor declares.
func TestTheToolsAreBoundToThePluginDescriptor(t *testing.T) {
	p := New()
	declared := map[string]bool{}
	for _, c := range p.Descriptor().Contributions {
		if c.Kind == plugin.ContributionTool {
			declared[c.ID] = true
		}
	}
	tools := p.Tools()
	if len(tools) != len(declared) {
		t.Fatalf("the plugin exposes %d tools, the descriptor declares %d", len(tools), len(declared))
	}
	for _, tool := range tools {
		if !declared[tool.Name()] {
			t.Fatalf("the plugin exposes %q, which the descriptor does not declare", tool.Name())
		}
	}
}

// ---- 小工具：真进程状态的观测 ----

func readPid(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", data, err)
	}
	return pid
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// waitUntilGone 断言进程真的没了：kill(pid, 0) 返回 ESRCH。被 Wait 回收之后立刻成立，
// 这里仍然轮询一小段时间，避免与回收的收尾竞争。
func waitUntilGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive after the command was stopped", pid)
}
