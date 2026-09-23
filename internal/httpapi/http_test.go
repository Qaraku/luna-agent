package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
)

type fakePlugins struct {
	state     pluginhost.State
	reloadErr error
}

func (f *fakePlugins) State() pluginhost.State              { return f.state }
func (f *fakePlugins) Reload(context.Context, string) error { return f.reloadErr }

type fakeRunner struct{ fail bool }

// Run mirrors the real runner's session contract: the run id and, when the run
// belongs to a session, the session id travel on the existing run.started event.
func (f fakeRunner) Run(_ context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.Event{Type: "run.started", Data: agent.RunStarted{RunID: req.RunID, SessionID: req.SessionID}})
	if f.fail {
		req.Sink.Emit(agent.Event{Type: "run.failed", Data: agent.RunFailed{RunID: req.RunID, Error: "model unavailable"}})
		return "", errors.New("model unavailable")
	}
	req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "hello"}})
	req.Sink.Emit(agent.Event{Type: "run.finished", Data: agent.RunFinished{RunID: req.RunID, Answer: "hello"}})
	return "hello", nil
}

// pluginState builds the plugin records for the named tools. Fixture plugin
// PIDs differ per tool, so an assertion on one of them cannot pass by accident
// on the other.
func pluginState(tools ...string) pluginhost.State {
	state := pluginhost.State{Plugins: []pluginhost.Record{}}
	for i, tool := range tools {
		state.Plugins = append(state.Plugins, pluginhost.Record{Tool: tool, Generation: 1, Version: "v1", Candidate: "v1", PluginPID: 123 + i, Status: "active"})
	}
	return state
}

// newTestStore is a real session store in a temporary directory, so the session
// API is exercised through its real implementation rather than a stub.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open session store: %v", err)
	}
	return s
}

func testHandler(t *testing.T, r Runner) http.Handler {
	t.Helper()
	return handlerWithStore(t, r, newTestStore(t))
}

func handlerWithStore(t *testing.T, r Runner, sessions Sessions) http.Handler {
	t.Helper()
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	return New(p, r, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"})
}

func request(t *testing.T, h http.Handler, method, path, body string, origin bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:43210"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:43210"
	if origin {
		r.Header.Set("Origin", "http://127.0.0.1:43210")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type timeoutRunner struct {
	deadline chan time.Time
}

func (r timeoutRunner) Run(ctx context.Context, _ agent.RunRequest) (string, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		r.deadline <- time.Time{}
		return "", errors.New("runner context has no deadline")
	}
	r.deadline <- deadline
	<-ctx.Done()
	return "", ctx.Err()
}

func TestRunTimeoutCoversRunnerInvocation(t *testing.T) {
	oldTimeout := runTimeout
	runTimeout = 20 * time.Millisecond
	defer func() { runTimeout = oldTimeout }()

	runner := timeoutRunner{deadline: make(chan time.Time, 1)}
	started := time.Now()
	w := request(t, testHandler(t, runner), http.MethodPost, "/api/runs", `{"message":"do it"}`, true)
	elapsed := time.Since(started)
	deadline := <-runner.deadline
	if deadline.IsZero() {
		t.Fatal("runner context had no deadline")
	}
	if elapsed > time.Second {
		t.Fatalf("run timeout was not bounded: %v", elapsed)
	}
	if strings.Count(w.Body.String(), "event: run.failed") != 1 {
		t.Fatalf("body=%s", w.Body.String())
	}
}

type cancelAwareRunner struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (r cancelAwareRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "late"}})
	return "", ctx.Err()
}

type disconnectWriter struct {
	mu                    sync.Mutex
	header                http.Header
	disconnected          bool
	writesAfterDisconnect int
}

func newDisconnectWriter() *disconnectWriter {
	return &disconnectWriter{header: make(http.Header)}
}
func (w *disconnectWriter) Header() http.Header { return w.header }
func (w *disconnectWriter) WriteHeader(int)     {}
func (w *disconnectWriter) Flush()              {}
func (w *disconnectWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disconnected {
		w.writesAfterDisconnect++
	}
	return len(p), nil
}
func (w *disconnectWriter) disconnect() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.disconnected = true
}
func (w *disconnectWriter) lateWrites() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writesAfterDisconnect
}

type queuedEventRunner struct {
	queue  chan struct{}
	queued chan struct{}
}

func (r queuedEventRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.Event{Type: "run.started", Data: agent.RunStarted{RunID: req.RunID}})
	<-r.queue
	req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "late"}})
	close(r.queued)
	<-ctx.Done()
	return "", ctx.Err()
}

type blockingWriter struct {
	mu      sync.Mutex
	header  http.Header
	entered chan struct{}
	release chan struct{}
	writes  int
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{
		header:  make(http.Header),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}
func (w *blockingWriter) Header() http.Header { return w.header }
func (w *blockingWriter) WriteHeader(int)     {}
func (w *blockingWriter) Flush()              {}
func (w *blockingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	first := w.writes == 1
	w.mu.Unlock()
	if first {
		close(w.entered)
		<-w.release
	}
	return len(p), nil
}
func (w *blockingWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func TestCanceledRequestDoesNotWriteQueuedSSEEvent(t *testing.T) {
	// Exercise the simultaneous-ready select repeatedly so the regression is
	// deterministic even though Go deliberately randomizes ready select cases.
	for attempt := 1; attempt <= 64; attempt++ {
		runner := queuedEventRunner{queue: make(chan struct{}), queued: make(chan struct{})}
		h := testHandler(t, runner)
		ctx, cancel := context.WithCancel(context.Background())
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/runs", strings.NewReader(`{"message":"do it"}`)).WithContext(ctx)
		r.Host = "127.0.0.1:43210"
		r.Header.Set("Origin", "http://127.0.0.1:43210")
		w := newBlockingWriter()
		done := make(chan struct{})
		go func() {
			h.ServeHTTP(w, r)
			close(done)
		}()

		select {
		case <-w.entered:
		case <-time.After(time.Second):
			cancel()
			close(w.release)
			t.Fatal("initial SSE write did not start")
		}
		close(runner.queue)
		select {
		case <-runner.queued:
		case <-time.After(time.Second):
			cancel()
			close(w.release)
			t.Fatal("later SSE event was not queued")
		}
		cancel()
		close(w.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("handler did not exit after cancellation")
		}
		if got := w.writeCount(); got != 1 {
			t.Fatalf("attempt %d: SSE write attempts=%d, want 1", attempt, got)
		}
	}
}

func TestCanceledRunWaitsForRunnerBeforeClearingBusyAndStopsWriting(t *testing.T) {
	runner := cancelAwareRunner{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	h := testHandler(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/runs", strings.NewReader(`{"message":"do it"}`)).WithContext(ctx)
	r.Host = "127.0.0.1:43210"
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	w := newDisconnectWriter()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, r)
		close(done)
	}()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	w.disconnect()
	cancel()
	select {
	case <-runner.canceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}

	stateResponse := request(t, h, http.MethodGet, "/api/state", "", false)
	var state State
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Busy {
		t.Fatal("busy cleared before runner exited")
	}

	close(runner.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not wait for runner exit")
	}
	if got := w.lateWrites(); got != 0 {
		t.Fatalf("writes after disconnect=%d", got)
	}
	stateResponse = request(t, h, http.MethodGet, "/api/state", "", false)
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Busy {
		t.Fatal("busy remained set after runner exit")
	}
}

type writeFailureRunner struct {
	canceled chan struct{}
	release  chan struct{}
}

func (r writeFailureRunner) Run(ctx context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.Event{Type: "run.started", Data: agent.RunStarted{RunID: req.RunID}})
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "late"}})
	return "", ctx.Err()
}

type failingWriter struct {
	mu     sync.Mutex
	header http.Header
	writes int
}

func newFailingWriter() *failingWriter       { return &failingWriter{header: make(http.Header)} }
func (w *failingWriter) Header() http.Header { return w.header }
func (w *failingWriter) WriteHeader(int)     {}
func (w *failingWriter) Flush()              {}
func (w *failingWriter) Write([]byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	return 0, errors.New("client write failed")
}
func (w *failingWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func TestSSEWriteFailureCancelsAndWaitsForRunner(t *testing.T) {
	runner := writeFailureRunner{canceled: make(chan struct{}), release: make(chan struct{})}
	h := testHandler(t, runner)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/runs", strings.NewReader(`{"message":"do it"}`))
	r.Host = "127.0.0.1:43210"
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	w := newFailingWriter()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, r)
		close(done)
	}()

	select {
	case <-runner.canceled:
	case <-time.After(time.Second):
		t.Fatal("write failure did not cancel runner")
	}
	stateResponse := request(t, h, http.MethodGet, "/api/state", "", false)
	var state State
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Busy {
		t.Fatal("busy cleared before runner exited")
	}

	close(runner.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not wait for runner exit")
	}
	if got := w.writeCount(); got != 1 {
		t.Fatalf("SSE write attempts=%d", got)
	}
}

func TestRunSSEHasOrderedSingleTerminalEvent(t *testing.T) {
	w := request(t, testHandler(t, fakeRunner{}), http.MethodPost, "/api/runs", `{"message":"do it"}`, true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	body := w.Body.String()
	for _, typ := range []string{"run.started", "assistant.delta", "run.finished"} {
		if !strings.Contains(body, "event: "+typ+"\n") {
			t.Fatalf("missing %s: %s", typ, body)
		}
	}
	if strings.Count(body, "event: run.finished") != 1 || strings.Contains(body, "event: run.failed") {
		t.Fatalf("terminal semantics: %s", body)
	}
	if strings.Index(body, "run.started") > strings.Index(body, "assistant.delta") || strings.Index(body, "assistant.delta") > strings.Index(body, "run.finished") {
		t.Fatalf("order: %s", body)
	}
}

func TestRunFailureHasSingleFailedTerminal(t *testing.T) {
	w := request(t, testHandler(t, fakeRunner{fail: true}), http.MethodPost, "/api/runs", `{"message":"do it"}`, true)
	body := w.Body.String()
	if strings.Count(body, "event: run.failed") != 1 || strings.Contains(body, "event: run.finished") {
		t.Fatalf("terminal semantics: %s", body)
	}
}

type malformedTerminalRunner struct {
	missing bool
}

func (r malformedTerminalRunner) Run(_ context.Context, req agent.RunRequest) (string, error) {
	req.Sink.Emit(agent.Event{Type: "run.started", Data: agent.RunStarted{RunID: req.RunID}})
	if r.missing {
		req.Sink.Emit(agent.Event{Type: "assistant.delta", Data: agent.AssistantDelta{Text: "hello"}})
		return "hello", nil
	}
	req.Sink.Emit(agent.Event{Type: "run.finished", Data: agent.RunFinished{RunID: req.RunID, Answer: "hello"}})
	req.Sink.Emit(agent.Event{Type: "run.finished", Data: agent.RunFinished{RunID: req.RunID, Answer: "duplicate"}})
	req.Sink.Emit(agent.Event{Type: "run.failed", Data: agent.RunFailed{RunID: req.RunID, Error: "late failure"}})
	return "hello", nil
}

func TestRunFiltersDuplicateTerminalEvents(t *testing.T) {
	w := request(t, testHandler(t, malformedTerminalRunner{}), http.MethodPost, "/api/runs", `{"message":"do it"}`, true)
	body := w.Body.String()
	terminalCount := strings.Count(body, "event: run.finished") + strings.Count(body, "event: run.failed")
	if terminalCount != 1 {
		t.Fatalf("terminal count=%d body=%s", terminalCount, body)
	}
}

func TestRunSynthesizesOneTerminalWhenRunnerOmitsIt(t *testing.T) {
	w := request(t, testHandler(t, malformedTerminalRunner{missing: true}), http.MethodPost, "/api/runs", `{"message":"do it"}`, true)
	body := w.Body.String()
	if strings.Count(body, "event: run.finished") != 1 || strings.Contains(body, "event: run.failed") {
		t.Fatalf("terminal semantics: %s", body)
	}
	if !strings.Contains(body, `"answer":"hello"`) {
		t.Fatalf("synthesized terminal missing answer: %s", body)
	}
}

func TestHTTPGuardsAndStrictBodies(t *testing.T) {
	h := testHandler(t, fakeRunner{})
	cases := []struct {
		name, method, path, body string
		origin                   bool
		host                     string
		want                     int
	}{
		{"null origin", http.MethodPost, "/api/runs", `{"message":"x"}`, false, "", 403},
		{"foreign origin", http.MethodPost, "/api/runs", `{"message":"x"}`, true, "foreign", 403},
		{"bad host", http.MethodGet, "/api/state", "", false, "bad", 403},
		{"unknown field", http.MethodPost, "/api/reload", `{"candidate":"v1","path":"/tmp/x"}`, true, "", 400},
		{"unknown candidate", http.MethodPost, "/api/reload", `{"candidate":"other"}`, true, "", 400},
		{"oversize", http.MethodPost, "/api/runs", `{"message":"` + strings.Repeat("x", 33000) + `"}`, true, "", 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:43210"+tc.path, strings.NewReader(tc.body))
			r.Host = "127.0.0.1:43210"
			if tc.origin {
				origin := "http://127.0.0.1:43210"
				if tc.host == "foreign" {
					origin = "https://evil.test"
				}
				r.Header.Set("Origin", origin)
			}
			if tc.host == "bad" {
				r.Host = "localhost:43210"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestStateAndHealthContainNoSecretAndHonestConnectionState(t *testing.T) {
	h := testHandler(t, fakeRunner{})
	w := request(t, h, http.MethodGet, "/api/state", "", false)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if strings.Contains(w.Body.String(), "api_key") {
		t.Fatalf("secret field in state: %s", w.Body.String())
	}
	var state State
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.ModelConnected || !state.ModelConfigured || state.Model != "fake-model" || len(state.Plugins) == 0 {
		t.Fatalf("bad state: %+v", state)
	}
	w = request(t, h, http.MethodGet, "/healthz", "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":true`) {
		t.Fatalf("health=%d %s", w.Code, w.Body.String())
	}
}

// /api/state reports every allowlisted tool, not one active entry: a single
// active record could not name which tool had failed.
func TestStateReportsEveryToolAndNoSingleActiveEntry(t *testing.T) {
	w := request(t, testHandler(t, fakeRunner{}), http.MethodGet, "/api/state", "", false)
	body := w.Body.String()
	if strings.Contains(body, `"active":{`) {
		t.Fatalf("state still carries a single active entry: %s", body)
	}
	var state State
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Plugins) != len(pluginhost.Allowlist) {
		t.Fatalf("plugins=%+v, want one record per allowlisted tool", state.Plugins)
	}
	for _, spec := range pluginhost.Allowlist {
		if !strings.Contains(body, `"tool":"`+spec.Tool+`"`) {
			t.Fatalf("state does not name %s: %s", spec.Tool, body)
		}
		found := false
		for _, record := range state.Plugins {
			if record.Tool == spec.Tool && record.Status == "active" {
				found = true
			}
		}
		if !found {
			t.Fatalf("state has no active record for %s: %+v", spec.Tool, state.Plugins)
		}
	}
}

// Readiness covers the whole tool set: one live tool is not enough.
func TestHealthzRequiresEveryAllowlistedTool(t *testing.T) {
	full := request(t, testHandler(t, fakeRunner{}), http.MethodGet, "/healthz", "", false)
	if !strings.Contains(full.Body.String(), `"plugin_active":true`) {
		t.Fatalf("health with every tool active: %s", full.Body.String())
	}
	partial := New(&fakePlugins{state: pluginState(pluginhost.ToolTextTransform)}, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"})
	w := request(t, partial, http.MethodGet, "/healthz", "", false)
	body := w.Body.String()
	if !strings.Contains(body, `"plugin_active":false`) || !strings.Contains(body, `"ready":false`) {
		t.Fatalf("a missing tool must make the host unready: %s", body)
	}
	if !strings.Contains(body, `"model_configured":true`) {
		t.Fatalf("readiness must still distinguish configuration from a live plugin: %s", body)
	}
}

func TestListenRejectsNonLiteralLoopback(t *testing.T) {
	for _, addr := range []string{"localhost:0", "0.0.0.0:0", "[::]:0"} {
		if l, err := Listen(addr); err == nil {
			l.Close()
			t.Fatalf("accepted %s", addr)
		}
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}
