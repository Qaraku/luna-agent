package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type usageTestModel struct {
	first, final []*schema.Message
	failFinal    bool
}

func (m usageTestModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m usageTestModel) messages(input []*schema.Message) ([]*schema.Message, error) {
	for _, msg := range input {
		if msg.Role == schema.Tool {
			if m.failFinal {
				return nil, errors.New("synthetic model failure")
			}
			return m.final, nil
		}
	}
	return m.first, nil
}
func (m usageTestModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	messages, err := m.messages(input)
	if err != nil {
		return nil, err
	}
	return schema.ConcatMessages(messages)
}
func (m usageTestModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	messages, err := m.messages(input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray(messages), nil
}
func usageChunk(input, output int) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output, PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 2}, CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 1}}}}
}
func TestUsageAccumulatesCallsButNotSnapshotsAndPersists(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		first, final                   []*schema.Message
		fail, complete                 bool
		input, output, reported, calls int
	}{
		{name: "two calls and repeated snapshots", first: []*schema.Message{usageChunk(10, 2), usageChunk(20, 3), usageChunk(20, 3)}, final: []*schema.Message{usageChunk(5, 7)}, input: 25, output: 10, reported: 2, calls: 2, complete: true},
		{name: "second call unreported", first: []*schema.Message{usageChunk(20, 3)}, input: 20, output: 3, reported: 1, calls: 2},
		{name: "second call fails", first: []*schema.Message{usageChunk(20, 3)}, fail: true, input: 20, output: 3, reported: 1, calls: 1},
		{name: "no report", calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disk, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id, err := disk.Create("usage")
			if err != nil {
				t.Fatal(err)
			}
			m := usageTestModel{first: append(tc.first, toolCallChunk()), final: append(tc.final, schema.AssistantMessage("answer", nil)), failFinal: tc.fail}
			runner, err := NewRunner(context.Background(), m, fakeInvoker{out: pluginhost.Output{Result: "ok"}}, &recordingReader{}, WithTranscript(disk))
			if err != nil {
				t.Fatal(err)
			}
			sink := &collectingSink{}
			_, err = runner.Run(context.Background(), RunRequest{RunID: "usage-run", SessionID: id, Message: "check", Sink: sink})
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			var event map[string]any
			for _, item := range sink.events {
				if item.Type == "usage.updated" {
					raw, err := json.Marshal(item.Data)
					if err != nil {
						t.Fatal(err)
					}
					json.Unmarshal(raw, &event)
				}
			}
			session, err := disk.Read(id)
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			for _, record := range session.Records {
				if record.Run != nil {
					raw, err := json.Marshal(record.Run)
					if err != nil {
						t.Fatal(err)
					}
					json.Unmarshal(raw, &saved)
				}
			}
			if tc.reported == 0 {
				if event != nil || saved["usage"] != nil {
					t.Fatal("missing usage became a fabricated report")
				}
				return
			}
			if event["scope"] != "run" || event["input_tokens"] != float64(tc.input) || event["output_tokens"] != float64(tc.output) || event["total_tokens"] != float64(tc.input+tc.output) || event["model_calls"] != float64(tc.calls) || event["reported_calls"] != float64(tc.reported) || event["complete"] != tc.complete {
				t.Errorf("event=%v", event)
			}
			stored, ok := saved["usage"].(map[string]any)
			if !ok {
				t.Fatal("run record did not preserve usage")
			}
			for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "model_calls", "reported_calls", "complete"} {
				if stored[key] != event[key] {
					t.Errorf("saved %s=%v, event=%v", key, stored[key], event[key])
				}
			}
		})
	}
}

func TestUsageInvalidReportsStayUnknownAndTotalsDoNotDoubleCountDetails(t *testing.T) {
	u := &runUsage{}
	u.begin()
	u.observe(&schema.TokenUsage{PromptTokens: -1, CompletionTokens: 5})
	if u.snapshot(true) != nil {
		t.Fatal("invalid report became known zero usage")
	}
	u.observe(&schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 999, PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: 8}, CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 4}})
	got := u.snapshot(false)
	if got == nil || got.TotalTokens != 15 || got.Complete || got.CachedTokens != 8 || got.ReasoningTokens != 4 {
		t.Fatalf("snapshot=%+v", got)
	}
}
