package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/config"
)

// reasoningEffortProbe serves one streaming answer and hands back the request body
// it was asked with. The level only exists on the wire, so the request itself is
// the only honest evidence that the knob works.
func reasoningEffortProbe(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	bodies := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request: %v", err)
		}
		bodies <- string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, chunk := range chunks {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, bodies
}

func runOnceWithEffort(t *testing.T, effort string) string {
	t.Helper()
	srv, bodies := reasoningEffortProbe(t)
	cfg := config.Config{BaseURL: srv.URL, APIKey: "test-key", Model: "test-model", ReasoningEffort: effort}
	r, err := NewOpenAIRunner(context.Background(), cfg, &fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatalf("building the runner: %v", err)
	}
	sink := &collectingSink{}
	if _, err := r.Run(context.Background(), RunRequest{Message: "say hi", RunID: "run-effort", Sink: sink}); err != nil {
		t.Fatalf("running: %v", err)
	}
	select {
	case body := <-bodies:
		return body
	default:
		t.Fatal("the provider was never called")
		return ""
	}
}

func TestReasoningEffortReachesTheProviderRequest(t *testing.T) {
	body := runOnceWithEffort(t, "high")
	if !strings.Contains(body, `"reasoning_effort":"high"`) {
		t.Fatalf("the request did not carry the chosen level: %s", body)
	}
}

// The default has to stay absent rather than defaulted on our side: a provider
// that does not define the field would reject a request that mentions it, so a
// knob nobody turned must not appear.
func TestReasoningEffortIsAbsentWhenNoLevelIsChosen(t *testing.T) {
	body := runOnceWithEffort(t, "")
	if strings.Contains(body, "reasoning_effort") {
		t.Fatalf("a level nobody chose was sent: %s", body)
	}
}
