package terminal

import (
	"context"
	"errors"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if code, handled := NetworkHelper(os.Args); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
func approvedContext(root string) context.Context {
	p := plugin.DefaultAccessPolicy()
	return plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p, Approve: func(context.Context, plugin.AccessRequest) error { return nil }}), []string{root})
}
func TestCommandWritesOnlyTheApprovedDirectory(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "out"), 0700)
	result, err := NewRunTool().Invoke(approvedContext(root), args(t, map[string]any{"command": "printf yes > out/result; printf no > forbidden", "write": true, "write_dirs": []string{"out"}}))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "out/result"))
	if err != nil || string(data) != "yes" {
		t.Fatalf("write=%q %v; %s", data, err, result)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("command wrote outside approved directory")
	}
}
func TestCommandCanRunWithoutProjectRead(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	secret := filepath.Join(root, "not-visible")
	os.WriteFile(secret, []byte("private-project"), 0600)
	p := plugin.DefaultAccessPolicy()
	p.Read = plugin.DecisionDeny
	p.Exec = plugin.DecisionAllow
	ctx := plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p}), []string{root})
	result, err := NewRunTool().Invoke(ctx, args(t, map[string]any{"command": "test ! -e " + shellQuote(secret) + " && printf scratch-only", "read": false}))
	if err != nil || !strings.Contains(result, "scratch-only") {
		t.Fatalf("result=%s err=%v", result, err)
	}
}
func TestCommandNetworkDeniedBeforeBridgeStarts(t *testing.T) {
	root := t.TempDir()
	tool := NewRunTool()
	tool.sandboxPath = "/must-not-start"
	p := plugin.DefaultAccessPolicy()
	p.Exec = plugin.DecisionAllow
	p.Network = plugin.DecisionDeny
	ctx := plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &p}), []string{root})
	_, err := tool.Invoke(ctx, args(t, map[string]any{"command": "printf denied", "network": true}))
	if !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("network did not reject before startup: %v", err)
	}
}
func TestCommandPublicProxyWorksWithoutHostNetwork(t *testing.T) {
	requireSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl fixture unavailable")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("public-proxy-fixture")) }))
	defer upstream.Close()
	address := strings.TrimPrefix(upstream.URL, "http://")
	tool := NewRunTool()
	tool.networkDial = func(ctx context.Context, target string) (net.Conn, error) {
		if target != "example.test:80" {
			return nil, errors.New("test target rejected")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
	root := t.TempDir()
	result, err := tool.Invoke(approvedContext(root), args(t, map[string]any{"command": "curl -fsS --max-time 3 http://example.test/ && ! curl --noproxy '*' -fsS --max-time 1 http://" + address, "network": true, "timeout_s": 6}))
	if err != nil || !strings.Contains(result, "public-proxy-fixture") || !strings.Contains(result, "exit code 0") {
		t.Fatalf("proxy result=%s err=%v", result, err)
	}
}
func TestCommandProxyRefusesHostControlAddress(t *testing.T) {
	requireSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl fixture unavailable")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("host control endpoint was reached") }))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(approvedContext(t.TempDir()), 8*time.Second)
	defer cancel()
	result, err := NewRunTool().Invoke(ctx, args(t, map[string]any{"command": "curl -sS --max-time 3 " + upstream.URL, "network": true}))
	if err != nil || !strings.Contains(result, "only public addresses") {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func TestCommandNetworkCancellationClosesExternalConnection(t *testing.T) {
	requireSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl fixture unavailable")
	}
	entered := make(chan struct{})
	closed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(closed) }))
	defer upstream.Close()
	address := strings.TrimPrefix(upstream.URL, "http://")
	tool := NewRunTool()
	tool.networkDial = func(ctx context.Context, target string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
	ctx, cancel := context.WithCancel(approvedContext(t.TempDir()))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tool.Invoke(ctx, args(t, map[string]any{"command": "curl --max-time 10 -sS http://example.test/wait", "network": true}))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("proxy request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("command did not stop")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("external socket remained open")
	}
}
