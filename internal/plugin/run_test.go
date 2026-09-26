package plugin

import (
	"context"
	"testing"
)

func TestRunInfoRoundTrips(t *testing.T) {
	ctx := WithRun(context.Background(), RunInfo{RunID: "run-1", SessionID: "session-1"})
	got, ok := Run(ctx)
	if !ok {
		t.Fatal("Run reported no run info after WithRun")
	}
	if got.RunID != "run-1" || got.SessionID != "session-1" {
		t.Fatalf("Run = %+v", got)
	}
}

func TestRunWithoutInfoReturnsZero(t *testing.T) {
	got, ok := Run(context.Background())
	if ok {
		t.Fatalf("Run claimed info on a bare context: %+v", got)
	}
	if got != (RunInfo{}) {
		t.Fatalf("Run = %+v, want the zero value", got)
	}
}

// 空会话是合法输入：宿主在没有会话的上下文里调用工具时，插件记到的就是空来源。
func TestRunInfoWithAnEmptySession(t *testing.T) {
	ctx := WithRun(context.Background(), RunInfo{RunID: "run-1"})
	got, ok := Run(ctx)
	if !ok || got.SessionID != "" {
		t.Fatalf("Run = %+v ok=%v", got, ok)
	}
}
