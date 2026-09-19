package pluginhost

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func testHost(t *testing.T, opts Options) *Host {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(context.Background(), root, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}

func TestRealSubprocessReplacementPinsInflightAndRollsBack(t *testing.T) {
	h := testHost(t, Options{})
	first := h.State().Active
	if first == nil || first.Version != "v1" || first.PluginPID == os.Getpid() {
		t.Fatalf("bad v1 state: %+v", first)
	}

	// Keep the old RPC in flight longer than a cached candidate build.
	done := make(chan Output, 1)
	errs := make(chan error, 1)
	go func() {
		out, err := h.Invoke(context.Background(), Input{Text: " first ", DelayMS: 3000})
		done <- out
		errs <- err
	}()
	waitFor(t, func() bool { a := h.State().Active; return a != nil && a.Inflight == 1 })
	if err := h.Reload(context.Background(), "v2"); err != nil {
		t.Fatal(err)
	}
	second := h.State().Active
	if second.Version != "v2" || second.Generation == first.Generation || second.PluginPID == first.PluginPID {
		t.Fatalf("bad replacement: %+v", second)
	}
	if len(h.State().Plugins) != 2 {
		t.Fatalf("old generation not retained: %+v", h.State().Plugins)
	}
	out2, err := h.Invoke(context.Background(), Input{Text: " hello ", DelayMS: 0})
	if err != nil || out2.Result != "Luna · HELLO" || out2.Generation != second.Generation {
		t.Fatalf("v2 output=%+v err=%v", out2, err)
	}
	out1 := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if out1.Result != "first" || out1.Generation != first.Generation || out1.PluginPID != first.PluginPID {
		t.Fatalf("lost pin: %+v", out1)
	}
	waitFor(t, func() bool { return len(h.State().Plugins) == 1 && syscall.Kill(first.PluginPID, 0) == syscall.ESRCH })
	before := *h.State().Active
	if err := h.Reload(context.Background(), "broken"); err == nil {
		t.Fatal("broken candidate accepted")
	}
	after := h.State().Active
	if after.Generation != before.Generation || after.PluginPID != before.PluginPID {
		t.Fatalf("rollback changed active: before=%+v after=%+v", before, after)
	}
}

func TestSameVersionReloadCreatesNewProcessAndCleansOld(t *testing.T) {
	h := testHost(t, Options{})
	old := *h.State().Active
	if err := h.Reload(context.Background(), "v1"); err != nil {
		t.Fatal(err)
	}
	cur := h.State().Active
	if cur.Generation == old.Generation || cur.PluginPID == old.PluginPID {
		t.Fatalf("same version was not rebuilt: old=%+v new=%+v", old, cur)
	}
	waitFor(t, func() bool { return syscall.Kill(old.PluginPID, 0) == syscall.ESRCH && len(h.State().Plugins) == 1 })
}

func TestInvokeTimeoutTerminatesOwnedPlugin(t *testing.T) {
	h := testHost(t, Options{RPCTimeout: 100 * time.Millisecond})
	pid := h.State().Active.PluginPID
	_, err := h.Invoke(context.Background(), Input{Text: "x", DelayMS: 500})
	if err == nil {
		t.Fatal("expected timeout")
	}
	waitFor(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH && h.State().Active == nil })
}

func TestRejectsUnknownCandidate(t *testing.T) {
	h := testHost(t, Options{})
	if err := h.Reload(context.Background(), "../../bin/sh"); err == nil {
		t.Fatal("unknown candidate accepted")
	}
}
