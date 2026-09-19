package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func newTestKernel(t *testing.T) *Kernel {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Close)
	return k
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(8 * time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestHotReloadDrainRollback(t *testing.T) {
	k := newTestKernel(t)
	// Warm the real v2 build so publishing can beat the 3-second RPC.
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "warm-v2"), "./plugins/v2")
	cmd.Dir = k.root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("warm build: %s: %v", out, err)
	}
	old := k.State().Active
	done := make(chan Output, 1)
	errs := make(chan error, 1)
	go func() { out, err := k.Invoke(Input{Text: " first ", DelayMS: 3000}); done <- out; errs <- err }()
	waitFor(t, func() bool { return k.State().Active.Inflight == 1 })
	if err := k.Reload(context.Background(), "v2"); err != nil {
		t.Fatal(err)
	}
	s := k.State()
	if s.HostPID != os.Getpid() || s.Active.Version != "v2" || s.Active.Generation == old.Generation || s.Active.PluginPID == old.PluginPID {
		t.Fatalf("bad swap %+v", s)
	}
	select {
	case <-done:
		t.Fatal("slow old call completed before v2 publish")
	default:
	}
	if len(s.Plugins) != 2 {
		t.Fatalf("must retain old process during RPC: %+v", s.Plugins)
	}
	out, err := k.Invoke(Input{Text: " next "})
	if err != nil || out.Result != "Luna · NEXT" {
		t.Fatalf("v2: %+v %v", out, err)
	}
	first := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if first.Generation != old.Generation || first.PluginPID != old.PluginPID || first.Result != "first" {
		t.Fatalf("lost pin: %+v", first)
	}
	waitFor(t, func() bool { return len(k.State().Plugins) == 1 && syscall.Kill(old.PluginPID, 0) == syscall.ESRCH })
	before := k.State().Active
	if err := k.Reload(context.Background(), "broken"); err == nil {
		t.Fatal("broken candidate accepted")
	}
	after := k.State().Active
	if after.Generation != before.Generation || after.PluginPID != before.PluginPID {
		t.Fatal("rollback changed active")
	}
	out, err = k.Invoke(Input{Text: "ok"})
	if err != nil || out.Result != "Luna · OK" {
		t.Fatalf("rollback invoke: %+v %v", out, err)
	}
	t.Logf("host=%d old_pid=%d new_pid=%d; pinned v1 drained and exited; broken rollback retained v2", s.HostPID, old.PluginPID, before.PluginPID)
}
func TestConcurrentRepeatedSwaps(t *testing.T) {
	k := newTestKernel(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				k.State()
				if _, err := k.Invoke(Input{Text: "x", DelayMS: 5}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for _, candidate := range []string{"v2", "v1", "v2", "v1"} {
		old := k.State().Active.PluginPID
		if err := k.Reload(context.Background(), candidate); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return syscall.Kill(old, 0) == syscall.ESRCH })
	}
	wg.Wait()
	waitFor(t, func() bool { return len(k.State().Plugins) == 1 })
}

func TestRealV1(t *testing.T) {
	k := newTestKernel(t)
	out, err := k.Invoke(Input{Text: "  hello Luna  "})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != "hello Luna" || out.Version != "v1" || out.Generation != 1 || out.PluginPID == os.Getpid() || out.PluginPID <= 0 {
		t.Fatalf("unexpected: %+v", out)
	}
	t.Logf("real subprocess: host=%d result=%+v", os.Getpid(), out)
}
