package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/config"
)

func TestSessionControlsReachProviderBeforeFirstMessage(t *testing.T) {
	type call struct {
		Model  string
		Effort *string `json:"reasoning_effort"`
	}
	calls := make(chan call, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got call
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
			http.Error(w, "bad test request", 400)
			return
		}
		calls <- got
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{"id": "fake", "object": "chat.completion.chunk", "model": got.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": "local answer"}, "finish_reason": "stop"}}}
		raw, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
	}))
	defer provider.Close()
	sessions := newTestStore(t)
	cfg := config.Config{BaseURL: provider.URL, APIKey: "synthetic-only", Model: "one", ReasoningEffort: "medium", Models: []config.Model{{Name: "one"}, {Name: "two"}}}
	runner, err := agent.NewOpenAIRunner(context.Background(), cfg, nil, nil, agent.WithHistory(sessions), agent.WithTranscript(sessions))
	if err != nil {
		t.Fatal(err)
	}
	h := New(&fakePlugins{state: everyAllowlistedTool()}, runner, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "one", ReasoningEffort: "medium", Models: []ModelRef{{Name: "one"}, {Name: "two"}}})
	made := controlsRequest(t, h, "/api/sessions", map[string]any{})
	if made.Code != 201 {
		t.Fatal(made.Body)
	}
	var created struct{ ID string }
	if err := json.Unmarshal(made.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	selected := controlsRequest(t, h, "/api/sessions/"+created.ID+"/model", map[string]any{"model": "two"})
	if selected.Code != 200 {
		t.Fatal(selected.Body)
	}
	for i, tc := range []struct {
		body    map[string]any
		want    string
		present bool
	}{
		{map[string]any{"reasoning_effort": "high"}, "high", true},
		{map[string]any{"reasoning_effort": ""}, "", false},
		{map[string]any{"reset": true}, "medium", true},
	} {
		choice := controlsRequest(t, h, "/api/sessions/"+created.ID+"/reasoning", tc.body)
		if choice.Code != 200 {
			t.Fatal(choice.Body)
		}
		result := controlsRequest(t, h, "/api/runs", map[string]any{"session_id": created.ID, "message": "第一条问题"})
		if result.Code != 200 {
			t.Fatalf("run %d: %d %s", i, result.Code, result.Body)
		}
		select {
		case got := <-calls:
			if got.Model != "two" || (got.Effort != nil) != tc.present || (got.Effort != nil && *got.Effort != tc.want) {
				t.Fatalf("run %d: model=%q effort=%v", i, got.Model, got.Effort)
			}
		case <-time.After(time.Second):
			t.Fatal("fake provider was not called")
		}
	}
	saved, err := sessions.Read(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "第一条问题" || saved.RunCount != 3 {
		t.Fatalf("title=%q runs=%d", saved.Title, saved.RunCount)
	}
}

func TestSessionControlMutationsRequireExactOrigin(t *testing.T) {
	h, sessions := modelsHandler(t)
	id, err := sessions.Create("origin")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/sessions", "/api/sessions/" + id + "/reasoning"} {
		got := request(t, h, http.MethodPost, path, "{}", false)
		if got.Code != 403 {
			t.Fatalf("unguarded %s: %d", path, got.Code)
		}
	}
}
