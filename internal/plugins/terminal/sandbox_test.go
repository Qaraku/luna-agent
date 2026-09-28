package terminal

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

func requireSandbox(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/bwrap", "--unshare-all", "--unshare-user", "--disable-userns", "--clearenv", "--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin", "--ro-bind", "/lib", "/lib", "--ro-bind", "/lib64", "/lib64", "--", "/usr/bin/true")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := cmd.CombinedOutput()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") || err != nil {
		if os.Getenv("LUNA_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatalf("required sandbox unavailable: %v: %s", err, output)
		}
		t.Skipf("sandbox integration unavailable: %v: %s", err, output)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func sandboxProbeBinary(t *testing.T, root string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(root, "probe")
	dest, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dest, source)
	closeErr := dest.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("copy probe: %v %v", err, closeErr)
	}
	return path
}

func probeCommand(binary, mode, target string) string {
	return "GORACE=atexit_sleep_ms=0 LUNA_SANDBOX_PROBE=1 " + shellQuote(binary) + " -test.run='^TestTerminalSandboxProbe$' -- " + shellQuote(mode) + " " + shellQuote(target)
}

// 该 helper 只在测试复制进工作区的二进制中运行，不读取真实用户数据。
func TestTerminalSandboxProbe(t *testing.T) {
	if os.Getenv("LUNA_SANDBOX_PROBE") != "1" {
		return
	}
	mode, target := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	switch mode {
	case "read":
		data, err := os.ReadFile(target)
		if err != nil {
			fmt.Print("PROBE_DENIED")
		} else {
			fmt.Print("PROBE_READ:", string(data))
		}
	case "proc-fds":
		entries, _ := os.ReadDir("/proc/1/fd")
		for _, entry := range entries {
			base := filepath.Join("/proc/1/fd", entry.Name())
			info, err := os.Stat(base)
			if err == nil && info.IsDir() {
				if data, err := os.ReadFile(filepath.Join(base, strings.TrimPrefix(target, "/"))); err == nil {
					fmt.Print("PROBE_READ:", string(data))
					os.Exit(0)
				}
			}
		}
		fmt.Print("PROBE_DENIED")
	case "write":
		if err := os.WriteFile(target, []byte("synthetic-write"), 0600); err != nil {
			fmt.Print("PROBE_DENIED")
		} else {
			fmt.Print("PROBE_WRITTEN")
		}
	case "tcp", "unix":
		conn, err := net.DialTimeout(mode, target, 200*time.Millisecond)
		if err != nil {
			fmt.Print("PROBE_DENIED")
		} else {
			conn.Close()
			fmt.Print("PROBE_CONNECTED")
		}
	case "pid":
		pid, _ := strconv.Atoi(target)
		if err := syscall.Kill(pid, 0); err != nil {
			fmt.Print("PROBE_DENIED")
		} else {
			fmt.Print("PROBE_VISIBLE")
		}
	case "local-tcp":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err == nil {
				conn.Close()
				fmt.Print("PROBE_LOCAL_NETWORK")
			}
			listener.Close()
		}
	case "socketpair":
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err == nil {
			syscall.Close(fds[0])
			syscall.Close(fds[1])
			fmt.Print("PROBE_LOCAL_IPC")
		}
	case "limits":
		var stat syscall.Statfs_t
		if syscall.Statfs("/tmp", &stat) == nil {
			fmt.Printf("TMP_LIMIT=%d", uint64(stat.Blocks)*uint64(stat.Bsize))
		}
	case "nested":
		tool := NewRunTool()
		result, err := tool.Invoke(runCtx(target), args(t, map[string]any{"command": probeCommand(filepath.Join(target, "probe"), "write", filepath.Join(target, "nested", "result"))}))
		if err != nil {
			fmt.Printf("INNER_FAILED: %v", err)
		} else {
			fmt.Print(result)
		}
	case "home":
		if os.Getenv("HOME") == "/tmp/home" && os.Getenv("LUNA_PRIVATE_TEST") == "" {
			fmt.Print("PROBE_PRIVATE_HOME")
		} else {
			fmt.Print("PROBE_HOST_ENV")
		}
	}
	os.Exit(0)
}

func isolatedResult(t *testing.T, root, command string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(runCtx(root), 5*time.Second)
	defer cancel()
	result, err := NewRunTool().Invoke(ctx, args(t, map[string]any{"command": command}))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSandboxBlocksFilesOutsideItsReadOnlyWorkspace(t *testing.T) {
	requireSandbox(t)
	root, outside := t.TempDir(), t.TempDir()
	binary := sandboxProbeBinary(t, root)
	allowed := filepath.Join(root, "allowed")
	secret := filepath.Join(outside, "synthetic-private")
	if err := os.WriteFile(allowed, []byte("workspace-input"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("outside-input"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if got := isolatedResult(t, root, probeCommand(binary, "read", allowed)); !strings.Contains(got, "PROBE_READ:workspace-input") {
		t.Fatalf("workspace unreadable: %s", got)
	}
	for _, path := range []string{secret, link, "/proc/1/root" + secret} {
		if got := isolatedResult(t, root, probeCommand(binary, "read", path)); !strings.Contains(got, "PROBE_DENIED") {
			t.Errorf("outside read succeeded: %s", got)
		}
	}
	if got := isolatedResult(t, root, probeCommand(binary, "proc-fds", secret)); !strings.Contains(got, "PROBE_DENIED") {
		t.Errorf("host root FD reachable: %s", got)
	}
	for _, path := range []string{filepath.Join(root, "new-file"), filepath.Join(outside, "new-file")} {
		if got := isolatedResult(t, root, probeCommand(binary, "write", path)); !strings.Contains(got, "PROBE_DENIED") {
			t.Errorf("host write succeeded: %s", got)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("a host file was created")
		}
	}
}

func TestSandboxKeepsTemporaryWritesAndHomePrivate(t *testing.T) {
	requireSandbox(t)
	root, scratch, home := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LUNA_PRIVATE_TEST", "synthetic-environment")
	binary := sandboxProbeBinary(t, root)
	if got := isolatedResult(t, root, probeCommand(binary, "home", "")); !strings.Contains(got, "PROBE_PRIVATE_HOME") {
		t.Fatalf("inherited host environment: %s", got)
	}
	path := filepath.Join(scratch, "result")
	command := "mkdir -p " + shellQuote(scratch) + "; printf temporary > " + shellQuote(path) + "; cat " + shellQuote(path)
	if got := isolatedResult(t, root, command); !strings.Contains(got, "temporary") {
		t.Fatalf("private scratch unusable: %s", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("temporary write reached the host")
	}
}

func TestSandboxCannotContactHostTCPOrWorkspaceUnixSockets(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	binary := sandboxProbeBinary(t, root)
	for _, mode := range []string{"tcp", "unix"} {
		t.Run(mode, func(t *testing.T) {
			address := "127.0.0.1:0"
			if mode == "unix" {
				address = filepath.Join(root, "host.sock")
			}
			listener, err := net.Listen(mode, address)
			if err != nil {
				t.Fatal(err)
			}
			var connections atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					c, e := listener.Accept()
					if e != nil {
						return
					}
					connections.Add(1)
					c.Close()
				}
			}()
			defer func() { listener.Close(); <-done }()
			got := isolatedResult(t, root, probeCommand(binary, mode, listener.Addr().String()))
			listener.Close()
			<-done
			if !strings.Contains(got, "PROBE_DENIED") || connections.Load() != 0 {
				t.Errorf("host socket reachable: count=%d %s", connections.Load(), got)
			}
		})
	}
}

func TestSandboxCannotSeeTheHostTestProcess(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	binary := sandboxProbeBinary(t, root)
	if got := isolatedResult(t, root, probeCommand(binary, "pid", strconv.Itoa(os.Getpid()))); !strings.Contains(got, "PROBE_DENIED") {
		t.Fatalf("host PID visible: %s", got)
	}
}

// 包装器属于测试夹具，只记录真实宿主 PID，再 exec 正式依赖；模型无法配置这个路径。
func trackedSandboxTool(t *testing.T) (*RunTool, string) {
	t.Helper()
	fixture := t.TempDir()
	pidPath := filepath.Join(fixture, "host.pid")
	wrapper := filepath.Join(fixture, "bwrap")
	text := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" > %s\nexec /usr/bin/bwrap \"$@\"\n", shellQuote(pidPath))
	if err := os.WriteFile(wrapper, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	tool := NewRunTool()
	tool.sandboxPath = wrapper
	return tool, pidPath
}

func TestSandboxAllowsOnlyItsLocalNetworkAndAnonymousIPC(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	binary := sandboxProbeBinary(t, root)
	for mode, want := range map[string]string{"local-tcp": "PROBE_LOCAL_NETWORK", "socketpair": "PROBE_LOCAL_IPC"} {
		if got := isolatedResult(t, root, probeCommand(binary, mode, "")); !strings.Contains(got, want) {
			t.Errorf("private %s is unusable: %s", mode, got)
		}
	}
	got := isolatedResult(t, root, "cat /proc/self/status")
	for _, expected := range []string{"NoNewPrivs:\t1", "Seccomp:\t2", "CapEff:\t0000000000000000"} {
		if !strings.Contains(got, expected) {
			t.Errorf("missing confinement property %q", expected)
		}
	}
}

func TestSandboxDependencyFailureNeverFallsBackToTheHostShell(t *testing.T) {
	for _, executable := range []string{filepath.Join(t.TempDir(), "absent"), "/usr/bin/false", "/usr/bin/true"} {
		root := t.TempDir()
		marker := filepath.Join(root, "must-not-exist")
		tool := NewRunTool()
		tool.sandboxPath = executable
		_, err := tool.Invoke(runCtx(root), args(t, map[string]any{"command": "printf escaped > " + shellQuote(marker)}))
		if !plugin.IsUnavailable(err) {
			t.Errorf("isolation failure was not unavailable: %v", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("fallback command wrote to the host")
		}
	}
}

func TestSandboxSetupFailureIsNotAnOrdinaryCommandExit(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	wrapper := filepath.Join(t.TempDir(), "bwrap")
	text := "#!/bin/sh\nrmdir " + shellQuote(root) + "\nexec /usr/bin/bwrap \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	tool := NewRunTool()
	tool.sandboxPath = wrapper
	_, err := tool.Invoke(runCtx(root), args(t, map[string]any{"command": "printf must-not-run"}))
	if !plugin.IsUnavailable(err) {
		t.Fatalf("setup failure was presented as a command result: %v", err)
	}
}

func TestSandboxNormalExitCleansDetachedBackgroundProcesses(t *testing.T) {
	requireSandbox(t)
	tool, pidFile := trackedSandboxTool(t)
	got, err := tool.Invoke(runCtx(t.TempDir()), args(t, map[string]any{"command": "setsid sh -c 'printf ready > /tmp/child-ready; sleep 30' & while [ ! -f /tmp/child-ready ]; do sleep 0.01; done; printf done", "timeout_s": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "exit code 0") || strings.Contains(got, "stopped") {
		t.Fatalf("a detached child held the run open: %s", got)
	}
	waitUntilGone(t, readPid(t, pidFile))
}

func TestSandboxRefusesRootsThatReplaceItsSecurityBoundary(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("Linux mount policy")
	}
	for _, root := range []string{"/", "/usr", "/etc", "/proc", "/dev", "/tmp"} {
		_, err := NewRunTool().Invoke(runCtx(root), args(t, map[string]any{"command": "printf must-not-run"}))
		if err == nil || plugin.IsUnavailable(err) {
			t.Errorf("overbroad root must be a policy refusal: %q %v", root, err)
		}
	}
}

func TestSandboxPrivateScratchIsBoundedAndUserNamespacesStayDisabled(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	binary := sandboxProbeBinary(t, root)
	if got := isolatedResult(t, root, probeCommand(binary, "limits", "")); !strings.Contains(got, fmt.Sprintf("TMP_LIMIT=%d", sandboxScratchBytes)) {
		t.Fatalf("scratch capacity was not enforced: %s", got)
	}
	if got := isolatedResult(t, root, "unshare -Ur /bin/true"); strings.Contains(got, "exit code 0") {
		t.Fatalf("nested user namespace was permitted: %s", got)
	}
}

func TestSandboxNestedWorkspaceMountsRemainReadOnly(t *testing.T) {
	requireSandbox(t)
	root, outside := t.TempDir(), t.TempDir()
	binary := sandboxProbeBinary(t, root)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	// 只在外层测试命名空间里构造嵌套 RW 挂载，不改变宿主挂载表。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/bwrap", "--unshare-user", "--unshare-pid", "--unshare-net", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--bind", root, root, "--bind", outside, nested, "--clearenv", "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "LUNA_SANDBOX_PROBE", "1", "--setenv", "GORACE", "atexit_sleep_ms=0", "--", binary, "-test.run=^TestTerminalSandboxProbe$", "--", "nested", root)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PROBE_DENIED") {
		t.Fatalf("nested mount was not confined: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(outside, "result")); !os.IsNotExist(err) {
		t.Error("nested host mount became writable")
	}
}

func TestSandboxRejectsAHostHomeInsideTheSystemRuntime(t *testing.T) {
	requireSandbox(t)
	t.Setenv("HOME", "/usr")
	_, err := NewRunTool().Invoke(runCtx(t.TempDir()), args(t, map[string]any{"command": "printf must-not-run"}))
	if !plugin.IsUnavailable(err) {
		t.Fatalf("host home would be included in runtime mounts: %v", err)
	}
}

func TestSandboxReadsAllDeclaredRootsWithoutGrantingWrites(t *testing.T) {
	requireSandbox(t)
	first, second := t.TempDir(), t.TempDir()
	path := filepath.Join(second, "input")
	if err := os.WriteFile(path, []byte("second-root"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(runCtx(first, second), 5*time.Second)
	defer cancel()
	got, err := NewRunTool().Invoke(ctx, args(t, map[string]any{"command": "cat " + shellQuote(path) + "; printf wrong > " + shellQuote(path)}))
	if err != nil || !strings.Contains(got, "second-root") {
		t.Fatalf("second root unreadable: %v %s", err, got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "second-root" {
		t.Fatal("second root was modified")
	}
}

func TestSandboxCancelledBeforeInvocationDoesNotStartAProcess(t *testing.T) {
	tool, pidFile := trackedSandboxTool(t)
	ctx, cancel := context.WithCancel(runCtx(t.TempDir()))
	cancel()
	_, err := tool.Invoke(ctx, args(t, map[string]any{"command": "printf must-not-run"}))
	if err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("cancelled call launched a process")
	}
}
