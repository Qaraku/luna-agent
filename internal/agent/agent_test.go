package agent

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"luna-agent/internal/pluginhost"
)

type fakeInvoker struct {
	out pluginhost.Output
	err error
}

func (f fakeInvoker) Invoke(context.Context, pluginhost.Input) (pluginhost.Output, error) {
	return f.out, f.err
}

type collectingSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *collectingSink) Emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func TestToolWrapperEmitsStartedThenFinishedWithActualMetadata(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	tool := NewTextTransformTool(fakeInvoker{out: pluginhost.Output{Result: "trimmed", Generation: 7, Version: "v2", PluginPID: 42}})
	got, err := tool.InvokableRun(ctx, `{"text":" hi "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "trimmed" {
		t.Fatalf("model-visible output must be the plain plugin result, got %q", got)
	}
	if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.finished" {
		t.Fatalf("event order: %+v", sink.events)
	}
	finished := sink.events[1].Data.(ToolFinished)
	if finished.Generation != 7 || finished.Version != "v2" || finished.PluginPID != 42 || finished.Result != "trimmed" {
		t.Fatalf("bad finished payload: %+v", finished)
	}
}

func TestToolHandsOnlyTheTransformedTextToTheModel(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	tool := NewTextTransformTool(fakeInvoker{out: pluginhost.Output{Result: "trimmed text", Generation: 7, Version: "v2", PluginPID: 42}})
	got, err := tool.InvokableRun(ctx, `{"text":" hi "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "trimmed text" {
		t.Fatalf("model-visible tool output must be the plain plugin result, got %q", got)
	}
	for _, leak := range []string{"42", "v2", "Generation", "generation", "PluginPID", "plugin_pid", "PID"} {
		if strings.Contains(got, leak) {
			t.Fatalf("model-visible tool output leaked plugin identity %q: %q", leak, got)
		}
	}
	if len(sink.events) != 2 {
		t.Fatalf("event count: %+v", sink.events)
	}
	finished, ok := sink.events[1].Data.(ToolFinished)
	if !ok {
		t.Fatalf("unexpected event payload: %#v", sink.events[1].Data)
	}
	if finished.Generation != 7 || finished.Version != "v2" || finished.PluginPID != 42 || finished.Result != "trimmed text" {
		t.Fatalf("plugin identity must stay exact in the UI event: %+v", finished)
	}
}

func TestToolRejectsTrailingJSON(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	tool := NewTextTransformTool(fakeInvoker{})

	if _, err := tool.InvokableRun(ctx, `{"text":"first"}{"text":"second"}`); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
	if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.failed" {
		t.Fatalf("events=%+v", sink.events)
	}
}

func TestToolSchemaIsStrict(t *testing.T) {
	info, err := NewTextTransformTool(fakeInvoker{}).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["type"] != "object" || raw["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %s", b)
	}
	req := raw["required"].([]any)
	if len(req) != 1 || req[0] != "text" {
		t.Fatalf("required mismatch: %s", b)
	}
}

type toolTurnModel struct {
	first []*schema.Message
	final string
}

func (m toolTurnModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m toolTurnModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	for _, msg := range input {
		if msg.Role == schema.Tool {
			return schema.AssistantMessage(m.final, nil), nil
		}
	}
	return schema.ConcatMessages(m.first)
}
func (m toolTurnModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	for _, msg := range input {
		if msg.Role == schema.Tool {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(m.final, nil)}), nil
		}
	}
	return schema.StreamReaderFromArray(m.first), nil
}

func toolCallChunk() *schema.Message {
	index := 0
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ToolName, Arguments: `{"text":"hello"}`}}}}
}

func assistantDeltaTexts(events []Event) []string {
	var texts []string
	for _, event := range events {
		if event.Type == "assistant.delta" {
			texts = append(texts, event.Data.(AssistantDelta).Text)
		}
	}
	return texts
}

func TestRunSuppressesStreamingContentBeforeToolCall(t *testing.T) {
	m := toolTurnModel{
		first: []*schema.Message{
			{Role: schema.Assistant, Content: "transient"},
			toolCallChunk(),
		},
		final: "final answer",
	}
	r, err := NewRunner(context.Background(), m, fakeInvoker{out: pluginhost.Output{Result: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), "transform", "run-1", sink)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "final answer" {
		t.Fatalf("answer=%q", answer)
	}
	if got := assistantDeltaTexts(sink.events); len(got) != 1 || got[0] != "final answer" {
		t.Fatalf("assistant deltas=%q", got)
	}
}

func TestRunSuppressesStreamingContentAfterToolCall(t *testing.T) {
	m := toolTurnModel{
		first: []*schema.Message{
			toolCallChunk(),
			{Role: schema.Assistant, Content: "transient"},
		},
		final: "final answer",
	}
	r, err := NewRunner(context.Background(), m, fakeInvoker{out: pluginhost.Output{Result: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), "transform", "run-1", sink)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "final answer" {
		t.Fatalf("answer=%q", answer)
	}
	if got := assistantDeltaTexts(sink.events); len(got) != 1 || got[0] != "final answer" {
		t.Fatalf("assistant deltas=%q", got)
	}
}

func newNonStreamingTestRunner(t *testing.T, m model.ToolCallingChatModel, invoker Invoker) *Runner {
	t.Helper()
	tt := NewTextTransformTool(invoker)
	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:          "luna-test",
		Instruction:   instruction,
		Model:         m,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{tt}}},
		MaxIterations: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{runner: adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: a, EnableStreaming: false})}
}

func TestRunSuppressesNonStreamingContentWithToolCall(t *testing.T) {
	m := toolTurnModel{
		first: []*schema.Message{{
			Role:      schema.Assistant,
			Content:   "transient",
			ToolCalls: toolCallChunk().ToolCalls,
		}},
		final: "final answer",
	}
	r := newNonStreamingTestRunner(t, m, fakeInvoker{out: pluginhost.Output{Result: "hello"}})
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), "transform", "run-1", sink)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "final answer" {
		t.Fatalf("answer=%q", answer)
	}
	if got := assistantDeltaTexts(sink.events); len(got) != 1 || got[0] != "final answer" {
		t.Fatalf("assistant deltas=%q", got)
	}
}

type sequentialProbeInvoker struct {
	mu            sync.Mutex
	active        int
	maxActive     int
	firstEntered  chan struct{}
	secondEntered chan struct{}
}

func newSequentialProbeInvoker() *sequentialProbeInvoker {
	return &sequentialProbeInvoker{firstEntered: make(chan struct{}), secondEntered: make(chan struct{})}
}

func (p *sequentialProbeInvoker) Invoke(ctx context.Context, in pluginhost.Input) (pluginhost.Output, error) {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()

	switch in.Text {
	case "first":
		close(p.firstEntered)
		select {
		case <-p.secondEntered:
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	case "second":
		select {
		case <-p.firstEntered:
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		close(p.secondEntered)
	}

	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return pluginhost.Output{Result: in.Text}, nil
}

func (p *sequentialProbeInvoker) maximumActive() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

func TestRunnerExecutesMultipleToolCallsSequentially(t *testing.T) {
	firstIndex, secondIndex := 0, 1
	m := toolTurnModel{
		first: []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{Index: &firstIndex, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ToolName, Arguments: `{"text":"first"}`}},
			{Index: &secondIndex, ID: "call-2", Type: "function", Function: schema.FunctionCall{Name: ToolName, Arguments: `{"text":"second"}`}},
		}}},
		final: "final answer",
	}
	invoker := newSequentialProbeInvoker()
	r, err := NewRunner(context.Background(), m, invoker)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	if _, err := r.Run(context.Background(), "transform twice", "run-1", sink); err != nil {
		t.Fatal(err)
	}
	if got := invoker.maximumActive(); got != 1 {
		t.Fatalf("maximum concurrent tool calls=%d", got)
	}
	var toolEvents []string
	for _, event := range sink.events {
		if event.Type == "tool.started" || event.Type == "tool.finished" {
			toolEvents = append(toolEvents, event.Type)
		}
	}
	want := []string{"tool.started", "tool.finished", "tool.started", "tool.finished"}
	if len(toolEvents) != len(want) {
		t.Fatalf("tool events=%v", toolEvents)
	}
	for i := range want {
		if toolEvents[i] != want[i] {
			t.Fatalf("tool events=%v", toolEvents)
		}
	}
}

type fakeModel struct{}

func (fakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if len(tools) != 1 || tools[0].Name != "luna_text_transform" {
		return nil, io.ErrUnexpectedEOF
	}
	return fakeModel{}, nil
}
func (fakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	for _, m := range input {
		if m.Role == schema.Tool {
			return schema.AssistantMessage("Observed transformed text.", nil), nil
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "luna_text_transform", Arguments: `{"text":" hello "}`}}}), nil
}
func (m fakeModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func TestFakeModelEinoEndToEndMapsAppEvents(t *testing.T) {
	root, _ := filepath.Abs("../..")
	h, err := pluginhost.New(context.Background(), root, pluginhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	r, err := NewRunner(context.Background(), fakeModel{}, h)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), "fake-model request", "run-e2e", sink)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Observed transformed text." {
		t.Fatalf("answer=%q", answer)
	}
	types := []string{}
	for _, e := range sink.events {
		types = append(types, e.Type)
	}
	want := []string{"run.started", "tool.started", "tool.finished", "assistant.delta", "run.finished"}
	if len(types) != len(want) {
		t.Fatalf("events=%v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events=%v", types)
		}
	}
	finished := sink.events[2].Data.(ToolFinished)
	if finished.Result != "hello" || finished.Generation != 1 || finished.Version != "v1" || finished.PluginPID <= 0 {
		t.Fatalf("tool event=%+v", finished)
	}
}
