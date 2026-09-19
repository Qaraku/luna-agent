package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Copy only backend source into a temporary module; never edit live candidates.
func fixtureRoot(t *testing.T, mode string) string {
	t.Helper()
	base, _ := filepath.Abs("../..")
	dir := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "internal/protocol/protocol.go", "plugins/v1/main.go"} {
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "plugins/v1/main.go" {
			source := string(data)
			if mode == "metadata" {
				source = strings.Replace(source, "return protocol.Metadata", `os.WriteFile("`+filepath.Join(dir, "pid")+`", []byte(fmt.Sprint(os.Getpid())),0600); time.Sleep(20*time.Second); return protocol.Metadata`, 1)
			}
			if mode == "invoke" {
				source = strings.Replace(source, "time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)", "time.Sleep(20*time.Second)", 1)
			}
			data = []byte(source)
		}
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func TestRealMetadataTimeout(t *testing.T) {
	dir := fixtureRoot(t, "metadata")
	start := time.Now()
	k, err := New(context.Background(), dir)
	if err == nil {
		k.Close()
		t.Fatal("hung metadata accepted")
	}
	if time.Since(start) > 12*time.Second {
		t.Fatal("startup exceeded bound")
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "pid"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, _ := strconv.Atoi(string(data))
	if syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatalf("hung candidate pid %d survived", pid)
	}
	t.Logf("hung metadata rejected in %s; pid %d exited", time.Since(start), pid)
}
func TestRealInvokeTimeout(t *testing.T) {
	dir := fixtureRoot(t, "invoke")
	k, err := New(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	pid := k.State().Active.PluginPID
	start := time.Now()
	_, err = k.Invoke(Input{Text: "hang"})
	if err == nil {
		t.Fatal("hung RPC succeeded")
	}
	if time.Since(start) > 9*time.Second {
		t.Fatal("RPC exceeded bound")
	}
	if syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatalf("hung pid %d survived", pid)
	}
	if k.State().Active != nil || len(k.State().Plugins) != 0 {
		t.Fatalf("failed generation retained: %+v", k.State())
	}
	t.Logf("hung invoke returned in %s after pid %d exited", time.Since(start), pid)
}
