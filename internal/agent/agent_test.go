package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type fakeInvoker struct {
	out pluginhost.Output
	err error
}

func (f fakeInvoker) Invoke(context.Context, pluginhost.Input) (pluginhost.Output, error) {
	return f.out, f.err
}

// recordingReader captures the read request the tool wrapper produced, so a
// test can prove the wrapper forwards the raw model-supplied path instead of
// validating it itself.
type recordingReader struct {
	requests []pluginhost.ReadRequest
	out      pluginhost.Output
	err      error
}

func (r *recordingReader) ReadFile(_ context.Context, req pluginhost.ReadRequest) (pluginhost.Output, error) {
	r.requests = append(r.requests, req)
	return r.out, r.err
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

// A malformed call is refused for the model rather than raised as a run error:
// the model wrote the arguments, so it is the one that can act on the reason
// while the run keeps going.
func TestToolRejectsTrailingJSON(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	tool := NewTextTransformTool(fakeInvoker{})

	got, err := tool.InvokableRun(ctx, `{"text":"first"}{"text":"second"}`)
	if err != nil {
		t.Fatalf("a refused call must not become a run error: %v", err)
	}
	if !strings.HasPrefix(got, refusalPrefix) {
		t.Fatalf("model-visible refusal = %q", got)
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
	r, err := NewRunner(context.Background(), m, fakeInvoker{out: pluginhost.Output{Result: "hello"}}, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), RunRequest{Message: "transform", RunID: "run-1", Sink: sink})
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
	r, err := NewRunner(context.Background(), m, fakeInvoker{out: pluginhost.Output{Result: "hello"}}, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), RunRequest{Message: "transform", RunID: "run-1", Sink: sink})
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
	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:          "luna-test",
		Instruction:   instruction,
		Model:         m,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{NewTextTransformTool(invoker), NewReadFileTool(&recordingReader{}), NewRememberTool(nil)}}},
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
	answer, err := r.Run(context.Background(), RunRequest{Message: "transform", RunID: "run-1", Sink: sink})
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
	r, err := NewRunner(context.Background(), m, invoker, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	if _, err := r.Run(context.Background(), RunRequest{Message: "transform twice", RunID: "run-1", Sink: sink}); err != nil {
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

// The core registers every model-visible tool itself: the two plugin-backed
// wrappers and the host-native memory tool, and nothing else. This Eino version
// passes the tool list to the model as a model option rather than calling this
// method, so the live observation of the offered tool set lives in
// TestTheModelSeesThreeToolsAndOnlyTheMemoryToolWrites; this stays as the
// contract for a version that does call it.
func (fakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	if len(names) != 3 || names[0] != "luna_text_transform" || names[1] != "luna_read_file" || names[2] != RememberToolName {
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
	r, err := NewRunner(context.Background(), fakeModel{}, h, h)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), RunRequest{Message: "fake-model request", RunID: "run-e2e", Sink: sink})
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

func TestReadFileToolEmitsStartedThenFinishedAndHidesIdentityFromTheModel(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	reader := &recordingReader{out: pluginhost.Output{Result: "file body", Generation: 9, Version: "v1", PluginPID: 77}}
	tool := NewReadFileTool(reader)
	got, err := tool.InvokableRun(ctx, `{"path":"docs/architecture.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "file body" {
		t.Fatalf("model-visible tool output must be the plain file text, got %q", got)
	}
	// The wrapper forwards the raw requested path; resolving it is the host's
	// job, never the wrapper's and never the plugin's.
	if len(reader.requests) != 1 || reader.requests[0].Path != "docs/architecture.md" {
		t.Fatalf("wrapper requests=%+v", reader.requests)
	}
	for _, leak := range []string{"77", "v1", "Generation", "generation", "PluginPID", "plugin_pid", "PID", "9"} {
		if strings.Contains(got, leak) {
			t.Fatalf("model-visible tool output leaked plugin identity %q: %q", leak, got)
		}
	}
	if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.finished" {
		t.Fatalf("event order: %+v", sink.events)
	}
	started, ok := sink.events[0].Data.(ToolStarted)
	if !ok || started.Name != ReadFileToolName {
		t.Fatalf("started payload: %#v", sink.events[0].Data)
	}
	args, ok := started.Arguments.(map[string]any)
	if !ok || args["path"] != "docs/architecture.md" {
		t.Fatalf("started arguments must carry the requested path: %#v", started.Arguments)
	}
	finished, ok := sink.events[1].Data.(ToolFinished)
	if !ok || finished.Name != ReadFileToolName {
		t.Fatalf("finished payload: %#v", sink.events[1].Data)
	}
	if finished.Result != "file body" || finished.Generation != 9 || finished.Version != "v1" || finished.PluginPID != 77 {
		t.Fatalf("plugin identity must stay exact in the UI event: %+v", finished)
	}
}

func TestReadFileToolRejectsMalformedArgumentsBeforeTheHost(t *testing.T) {
	for _, arguments := range []string{`{}`, `{"path":""}`, `{"path":"a"}{"path":"b"}`, `{"path":"a","depth":2}`, `not json`, ``} {
		sink := &collectingSink{}
		ctx := WithRun(context.Background(), "run-1", sink)
		reader := &recordingReader{out: pluginhost.Output{Result: "should not be reached"}}
		got, err := NewReadFileTool(reader).InvokableRun(ctx, arguments)
		if err != nil {
			t.Fatalf("arguments %q became a run error instead of a refusal: %v", arguments, err)
		}
		if !strings.HasPrefix(got, refusalPrefix) {
			t.Fatalf("arguments %q refusal = %q", arguments, got)
		}
		if len(reader.requests) != 0 {
			t.Fatalf("arguments %q reached the host: %+v", arguments, reader.requests)
		}
		if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.failed" {
			t.Fatalf("arguments %q events=%+v", arguments, sink.events)
		}
		failed := sink.events[1].Data.(ToolFailed)
		if failed.Name != ReadFileToolName || failed.Error == "" {
			t.Fatalf("arguments %q failed payload=%+v", arguments, failed)
		}
	}
}

func TestReadFileSchemaIsStrict(t *testing.T) {
	info, err := NewReadFileTool(&recordingReader{}).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != ReadFileToolName {
		t.Fatalf("tool name = %q", info.Name)
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
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("required mismatch: %s", b)
	}
}

// A refusal by the host (here: a file the read root does not hold) is handed to
// the model as the tool's result, so the run continues and the user gets an
// explanation instead of a failed run. The UI still sees tool.failed with the
// host's own message.
func TestReadFileToolHandsAHostRefusalToTheModel(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	reader := &recordingReader{err: fmt.Errorf("%w: %q", fileread.ErrNotFound, "TMP.md")}
	got, err := NewReadFileTool(reader).InvokableRun(ctx, `{"path":"TMP.md"}`)
	if err != nil {
		t.Fatalf("a refused read must not become a run error: %v", err)
	}
	if !strings.HasPrefix(got, refusalPrefix) || !strings.Contains(got, fileread.ErrNotFound.Error()) {
		t.Fatalf("model-visible refusal = %q", got)
	}
	if !strings.Contains(got, "TMP.md") {
		t.Fatalf("the refusal must name the path the model asked for: %q", got)
	}
	for _, leak := range []string{"Generation", "generation", "PluginPID", "plugin_pid", "v1", "v2"} {
		if strings.Contains(got, leak) {
			t.Fatalf("refusal leaked plugin identity %q: %q", leak, got)
		}
	}
	if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.failed" {
		t.Fatalf("events=%+v", sink.events)
	}
	failed := sink.events[1].Data.(ToolFailed)
	if failed.Name != ReadFileToolName || failed.Error == "" {
		t.Fatalf("failed payload=%+v", failed)
	}
}

// Infrastructure failures still fail the run: the tool never ran, so the model
// has nothing to explain and the failure belongs to the system rather than to
// the call the model made.
func TestPluginBackedToolsKeepInfrastructureFailuresFatal(t *testing.T) {
	for _, infra := range []error{pluginhost.ErrUnknownTool, pluginhost.ErrNoActivePlugin, pluginhost.ErrRPCTimeout, pluginhost.ErrRPCCanceled, pluginhost.ErrPluginGone} {
		sink := &collectingSink{}
		ctx := WithRun(context.Background(), "run-1", sink)
		if _, err := NewReadFileTool(&recordingReader{err: infra}).InvokableRun(ctx, `{"path":"docs/architecture.md"}`); !errors.Is(err, infra) {
			t.Fatalf("read_file error = %v, want %v to stay fatal", err, infra)
		}
		if _, err := NewTextTransformTool(fakeInvoker{err: infra}).InvokableRun(ctx, `{"text":"x"}`); !errors.Is(err, infra) {
			t.Fatalf("text_transform error = %v, want %v to stay fatal", err, infra)
		}
		if len(sink.events) == 0 || sink.events[len(sink.events)-1].Type != "tool.failed" {
			t.Fatalf("infrastructure failure events=%+v", sink.events)
		}
	}
}

type readModel struct{}

func (readModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return readModel{}, nil
}
func (readModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	for _, m := range input {
		if m.Role == schema.Tool {
			return schema.AssistantMessage("Observed the file.", nil), nil
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ReadFileToolName, Arguments: `{"path":"docs/architecture.md"}`}}}), nil
}
func (m readModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// The second tool runs through the real subprocess plugin behind Eino, and its
// event mapping is the same app-owned shape as the transform tool.
func TestFakeModelReadsAFileThroughEinoEndToEnd(t *testing.T) {
	root, _ := filepath.Abs("../..")
	h, err := pluginhost.New(context.Background(), root, pluginhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	r, err := NewRunner(context.Background(), readModel{}, h, h)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), RunRequest{Message: "read docs/architecture.md", RunID: "run-read", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Observed the file." {
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
	started := sink.events[1].Data.(ToolStarted)
	if started.Name != ReadFileToolName {
		t.Fatalf("started=%+v", started)
	}
	finished := sink.events[2].Data.(ToolFinished)
	if finished.Name != ReadFileToolName || !strings.Contains(finished.Result, "# Luna Agent architecture") {
		t.Fatalf("finished=%+v", finished)
	}
	if finished.Generation != 1 || finished.Version != "v1" || finished.PluginPID <= 0 {
		t.Fatalf("finished identity=%+v", finished)
	}
}

// refusalAwareModel calls the file tool once and then answers; it records what
// the tool result actually said, so a test can prove the model saw the refusal
// rather than a failed run.
type refusalAwareModel struct{ toolResult string }

func (m *refusalAwareModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *refusalAwareModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	for _, msg := range input {
		if msg.Role == schema.Tool {
			m.toolResult = msg.Content
			return schema.AssistantMessage("I could not read that file.", nil), nil
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{
		{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ReadFileToolName, Arguments: `{"path":"TMP.md"}`}},
	}), nil
}
func (m *refusalAwareModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// The user-visible defect this guards: asking for a file that is not there used
// to end the run with the host's raw error and no answer at all.
func TestARefusedToolCallStillAnswersTheUser(t *testing.T) {
	modelUnderTest := &refusalAwareModel{}
	runner, err := NewRunner(context.Background(), modelUnderTest, fakeInvoker{}, &recordingReader{err: fileread.ErrNotFound})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := runner.Run(context.Background(), RunRequest{Message: "read TMP.md", RunID: "run-refused", Sink: sink})
	if err != nil {
		t.Fatalf("a refused read must not fail the run: %v", err)
	}
	if answer != "I could not read that file." {
		t.Fatalf("answer=%q", answer)
	}
	if !strings.Contains(modelUnderTest.toolResult, refusalPrefix) || !strings.Contains(modelUnderTest.toolResult, fileread.ErrNotFound.Error()) {
		t.Fatalf("the model did not receive the refusal: %q", modelUnderTest.toolResult)
	}
	types := []string{}
	for _, e := range sink.events {
		types = append(types, e.Type)
	}
	want := []string{"run.started", "tool.started", "tool.failed", "assistant.delta", "run.finished"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v", types)
	}
}
