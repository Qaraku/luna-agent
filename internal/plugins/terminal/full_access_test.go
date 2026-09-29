package terminal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

func fullContext(root string) context.Context {
	return plugin.WithRun(runCtx(root), plugin.RunInfo{ExecutionMode: plugin.ExecutionFullAccess})
}
func TestFullAccessCanWriteOutsideWorkspaceAndUseAbsoluteCwd(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "result")
	tool := NewRunTool()
	// 显式 Full access 不依赖 Bubblewrap，也不能被混同成隔离启动失败时的后备。
	tool.sandboxPath = filepath.Join(t.TempDir(), "missing-bwrap")
	result, err := tool.Invoke(fullContext(root), args(t, map[string]any{"command": "printf authorized > " + shellQuote(path), "cwd": outside}))
	if err != nil || !strings.Contains(result, "exit code 0") {
		t.Fatalf("full access command: %v %s", err, result)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "authorized" {
		t.Fatalf("outside write did not happen: %v", err)
	}
}
func TestFullAccessCanReachOwnedHostListener(t *testing.T) {
	root := t.TempDir()
	binary := sandboxProbeBinary(t, root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connected := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
			close(connected)
		}
	}()
	result, err := NewRunTool().Invoke(fullContext(root), args(t, map[string]any{"command": probeCommand(binary, "tcp", listener.Addr().String())}))
	if err != nil || !strings.Contains(result, "PROBE_CONNECTED") {
		t.Fatalf("host network unavailable: %v %s", err, result)
	}
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("listener saw no connection")
	}
}
func TestToolArgumentsCannotSelectExecutionMode(t *testing.T) {
	_, err := NewRunTool().Invoke(runCtx(t.TempDir()), args(t, map[string]any{"command": "printf forbidden", "execution_mode": "full_access"}))
	if err == nil || plugin.IsUnavailable(err) {
		t.Fatalf("mode argument not refused: %v", err)
	}
}
func TestFullAccessPrecancelDoesNotStartACommand(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "started")
	ctx, cancel := context.WithCancel(fullContext(root))
	cancel()
	_, err := NewRunTool().Invoke(ctx, args(t, map[string]any{"command": "printf bad > " + shellQuote(marker)}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("pre-cancelled command ran")
	}
}

func TestTerminalContextReportsTheAuthorizedMode(t *testing.T) {
	p := New()
	provider, ok := any(p).(plugin.ContextProvider)
	if !ok {
		t.Fatal("terminal must tell the model which execution mode is authorized")
	}
	for _, mode := range []plugin.ExecutionMode{plugin.ExecutionSandbox, plugin.ExecutionFullAccess} {
		ctx := plugin.WithRun(runCtx(t.TempDir()), plugin.RunInfo{ExecutionMode: mode})
		blocks, err := provider.Contexts(ctx)
		if err != nil || len(blocks) != 1 || !strings.Contains(blocks[0].Text, string(mode)) {
			t.Fatalf("mode %s: blocks=%v error=%v", mode, blocks, err)
		}
	}
}

func TestFullAccessTimeoutAndCancellationKeepTheExistingLifecycle(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelled), func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "pid")
			ctx, cancel := context.WithCancel(fullContext(root))
			defer cancel()
			done := make(chan struct{})
			var result string
			var runErr error
			go func() {
				result, runErr = NewRunTool().Invoke(ctx, args(t, map[string]any{"command": "echo $$ > " + shellQuote(pidFile) + "; exec sleep 30", "timeout_s": 1}))
				close(done)
			}()
			var pid int
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(pidFile)
				if err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
					if pid > 0 {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("command never started")
			}
			if cancelled {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("command was not stopped")
			}
			if cancelled {
				if !errors.Is(runErr, context.Canceled) {
					t.Fatalf("cancel=%v", runErr)
				}
			} else if runErr != nil || !strings.Contains(result, "stopped") {
				t.Fatalf("timeout: %v %s", runErr, result)
			}
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatal("owned command survived stop")
			}
		})
	}
}

func TestFullAccessDoesNotAutomaticallyInheritUnrelatedEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LUNA_PRIVATE_TEST", "synthetic-secret")
	got, err := NewRunTool().Invoke(fullContext(t.TempDir()), args(t, map[string]any{"command": "test -z \"$LUNA_PRIVATE_TEST\" && test \"$HOME\" = " + shellQuote(home) + " && printf minimal-host-environment"}))
	if err != nil || !strings.Contains(got, "minimal-host-environment") {
		t.Fatalf("unexpected host environment: %v %s", err, got)
	}
}
