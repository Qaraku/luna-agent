package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
)

// completionWriter 在事件已 flush 时观察流，模拟客户端收到终止事件就发下一句。
// 回调只在 handler 所在 goroutine 执行，不从其他 goroutine 读取 recorder。
type completionWriter struct {
	*httptest.ResponseRecorder
	onFlush func(string)
}

func (w *completionWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.onFlush(w.Body.String())
}

func completionRequest(sessionID string) *http.Request {
	body := `{"message":"first","session_id":"` + sessionID + `"}`
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/runs", strings.NewReader(body))
	r.Host = "127.0.0.1:43210"
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	return r
}

func waitCompletion(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for run lifecycle checkpoint")
	}
}

type cancelledCompletionRunner struct{}

func (cancelledCompletionRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.TerminalEvent(ctx, req.RunID, "", context.Canceled))
	return "", context.Canceled
}

func TestTerminalFlushAdmitsTheNextRun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		runner   Runner
		terminal string
	}{
		{"finished", fakeRunner{}, "run.finished"},
		{"failed", fakeRunner{fail: true}, "run.failed"},
		{"cancelled", cancelledCompletionRunner{}, "run.cancelled"},
		{"synthesized", malformedTerminalRunner{missing: true}, "run.finished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := newTestStore(t)
			sessionID, err := sessions.Create("continuous conversation")
			if err != nil {
				t.Fatal(err)
			}
			h := handlerWithStore(t, tc.runner, sessions)
			nextStatus := 0
			w := &completionWriter{ResponseRecorder: httptest.NewRecorder()}
			w.onFlush = func(body string) {
				if nextStatus != 0 || !strings.Contains(body, "event: "+tc.terminal+"\n") {
					return
				}
				nextStatus = request(t, h, http.MethodPost, "/api/runs", `{"message":"next","session_id":"`+sessionID+`"}`, true).Code
			}
			h.ServeHTTP(w, completionRequest(sessionID))
			if nextStatus != http.StatusOK {
				t.Fatalf("next run at terminal flush: got HTTP %d, want 200", nextStatus)
			}
			if strings.Count(w.Body.String(), "event: "+tc.terminal+"\n") != 1 {
				t.Fatalf("expected one terminal event: %s", w.Body.String())
			}
		})
	}
}

type earlyCompletionRunner struct{ release <-chan struct{} }

func (r earlyCompletionRunner) Run(_ context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.Event{Type: "run.finished", Data: agent.RunFinished{RunID: req.RunID, Answer: "done"}})
	// FIFO 中该事件被处理，意味着 handler 已经收到前面的 terminal；无需靠 sleep 猜时序。
	req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "checkpoint"}})
	<-r.release
	return "done", nil
}

func TestTerminalWaitsForRunnerAndAllNonterminalEvents(t *testing.T) {
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	h := testHandler(t, earlyCompletionRunner{release: release})
	checkpoint, done := make(chan struct{}), make(chan struct{})
	var sawCheckpoint bool
	var terminalSeen atomic.Bool
	w := &completionWriter{ResponseRecorder: httptest.NewRecorder()}
	w.onFlush = func(body string) {
		if strings.Contains(body, "event: run.finished\n") {
			terminalSeen.Store(true)
		}
		if !sawCheckpoint && strings.Contains(body, "checkpoint") {
			sawCheckpoint = true
			close(checkpoint)
		}
	}
	go func() {
		h.ServeHTTP(w, completionRequest(""))
		close(done)
	}()
	waitCompletion(t, checkpoint)
	if terminalSeen.Load() {
		t.Error("terminal reached the client before the runner returned")
	}
	if next := request(t, h, http.MethodPost, "/api/runs", `{"message":"too soon"}`, true); next.Code != http.StatusConflict {
		t.Errorf("runner still executing: got HTTP %d, want 409", next.Code)
	}
	// 让 Runner 返回，且直到 handler 退出才读取 recorder。
	unblock()
	waitCompletion(t, done)
	body := w.Body.String()
	if !terminalSeen.Load() || strings.Index(body, "event: run.finished") < strings.Index(body, "checkpoint") {
		t.Errorf("terminal must follow every nonterminal event: %s", body)
	}
}

type heldSecondRun struct {
	calls   atomic.Int32
	started chan struct{}
	release <-chan struct{}
}

func (r *heldSecondRun) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	if r.calls.Add(1) == 2 {
		close(r.started)
		<-r.release
	}
	return (fakeRunner{}).Run(ctx, req)
}

func TestOldHandlerCleanupDoesNotReleaseTheNextRun(t *testing.T) {
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	runner := &heldSecondRun{started: make(chan struct{}), release: release}
	h := testHandler(t, runner)
	nextDone := make(chan struct{})
	var next *httptest.ResponseRecorder
	launched := false
	w := &completionWriter{ResponseRecorder: httptest.NewRecorder()}
	w.onFlush = func(body string) {
		if launched || !strings.Contains(body, "event: run.finished\n") {
			return
		}
		launched = true
		go func() {
			next = request(t, h, http.MethodPost, "/api/runs", `{"message":"second"}`, true)
			close(nextDone)
		}()
		select {
		case <-runner.started:
		case <-nextDone:
			t.Errorf("second run was rejected at terminal flush: HTTP %d", next.Code)
		case <-time.After(3 * time.Second):
			t.Error("second run did not reach admission")
		}
	}
	h.ServeHTTP(w, completionRequest(""))
	if !launched {
		t.Fatal("first run never emitted its terminal event")
	}
	select {
	case <-runner.started:
	default:
		return // 准入错误已报告，不再向未启动的 Runner 等待。
	}
	// 第一条 handler 的 defer 已执行；第二条仍在运行，第三条必须被拒绝。
	if third := request(t, h, http.MethodPost, "/api/runs", `{"message":"third"}`, true); third.Code != http.StatusConflict {
		t.Errorf("old cleanup released the active second run: got HTTP %d, want 409", third.Code)
	}
	unblock()
	waitCompletion(t, nextDone)
	if next.Code != http.StatusOK {
		t.Errorf("second run: HTTP %d", next.Code)
	}
}
