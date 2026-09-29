package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/config"
)

func TestSessionReasoningSelectionChangesTheActualProviderRequest(t *testing.T) {
	srv, bodies := reasoningEffortProbe(t)
	cfg := config.Config{BaseURL: srv.URL, APIKey: "synthetic-key", Model: "test-model", ReasoningEffort: "medium"}
	runner, err := NewOpenAIRunner(context.Background(), cfg, &fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	high, xhigh, max, off, none := "high", "xhigh", "max", "", "none"
	for i, tc := range []struct {
		choice  *string
		want    string
		present bool
	}{
		{&high, "high", true}, {&xhigh, "xhigh", true}, {&max, "max", true}, {&off, "", false}, {nil, "medium", true}, {&none, "none", true}, {nil, "medium", true},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := runner.Run(ctx, RunRequest{Message: "hello", RunID: "reasoning-test", ReasoningEffort: tc.choice, Sink: &collectingSink{}})
		cancel()
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		select {
		case body := <-bodies:
			var data map[string]any
			if err := json.Unmarshal([]byte(body), &data); err != nil {
				t.Fatal(err)
			}
			got, present := data["reasoning_effort"]
			if present != tc.present || (present && got != tc.want) {
				t.Errorf("run %d effort=%v present=%v, want=%q present=%v", i, got, present, tc.want, tc.present)
			}
		case <-time.After(time.Second):
			t.Fatal("fake provider received no request")
		}
	}
}
