package kernel

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupWatchStoppedBeforeCancel(t *testing.T) {
	for i := 0; i < 1000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		var killed atomic.Bool
		stop := watchStartup(ctx, func() { killed.Store(true) })
		stop()
		cancel()
		if killed.Load() {
			t.Fatal("healthy plugin killed after successful startup")
		}
	}
}
func TestStartupWatchEnforcesDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	killed := make(chan struct{})
	stop := watchStartup(ctx, func() { close(killed) })
	defer stop()
	select {
	case <-killed:
	case <-time.After(time.Second):
		t.Fatal("watchdog failed")
	}
}
