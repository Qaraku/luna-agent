package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

type delayedStopInvoker struct{ entered, release chan struct{} }

func (i *delayedStopInvoker) Invoke(ctx context.Context, _ pluginhost.Input) (pluginhost.Output, error) {
	close(i.entered)
	<-ctx.Done()
	<-i.release
	return pluginhost.Output{}, ctx.Err()
}
func TestRunWaitsForToolCleanupBeforePublishingTerminal(t *testing.T) {
	invoker := &delayedStopInvoker{entered: make(chan struct{}), release: make(chan struct{})}
	runner, err := NewRunner(context.Background(), transformCallModel{}, invoker, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx, RunRequest{RunID: "drain", Message: "run", Sink: sink}) }()
	select {
	case <-invoker.entered:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	early := false
	select {
	case <-done:
		early = true
	case <-time.After(30 * time.Millisecond):
	}
	close(invoker.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not finish cleanup")
	}
	if early {
		t.Fatal("run ended while a tool was still cleaning up")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	events := sink.events
	if len(events) < 3 || events[len(events)-1].Type != "run.cancelled" || events[len(events)-2].Type != "tool.failed" {
		t.Fatalf("event order=%+v", events)
	}
}

func TestClosingToolAdmissionWaitsForCallsAndRejectsLateCalls(t *testing.T) {
	drain := &toolDrain{}
	ctx := context.WithValue(context.Background(), toolDrainKey{}, drain)
	release, err := trackTool(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { drain.closeAndWait(); close(done) }()
	select {
	case <-done:
		t.Fatal("drain completed while a call was active")
	case <-time.After(10 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish")
	}
	if _, err := trackTool(ctx); err != context.Canceled {
		t.Fatalf("late call admitted: %v", err)
	}
}
