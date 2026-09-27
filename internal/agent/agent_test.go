package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// recordingReader captures the read and list requests the tool wrappers
// produced, so a test can prove a wrapper forwards the raw model-supplied path
// instead of validating it itself. Reads and listings are recorded and can fail
// separately: a test that makes one refuse must not accidentally make the other
// refuse too.
type recordingReader struct {
	requests     []pluginhost.ReadRequest
	out          pluginhost.Output
	err          error
	listRequests []pluginhost.ListRequest
	listOut      pluginhost.Output
	listErr      error
}

func (r *recordingReader) ReadFile(_ context.Context, req pluginhost.ReadRequest) (pluginhost.Output, error) {
	r.requests = append(r.requests, req)
	return r.out, r.err
}

func (r *recordingReader) ListDir(_ context.Context, req pluginhost.ListRequest) (pluginhost.Output, error) {
	r.listRequests = append(r.listRequests, req)
	return r.listOut, r.listErr
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

// TestToolCallTurnTextBeforeTheToolCallIsStreamed is the twin of the test below
// for the other order a provider may use inside one turn: the text arrives
// before the tool call. Both orders stream the text, and in neither order does it
// become the answer.
func TestToolCallTurnTextBeforeTheToolCallIsStreamed(t *testing.T) {
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
	if got := assistantDeltaTexts(sink.events); strings.Join(got, ",") != "transient,final answer" {
		t.Fatalf("assistant deltas=%q", got)
	}
}

// TestToolCallTurnTextIsStreamedButIsNotTheAnswer pins both halves of the
// streaming contract: what a tool-call turn writes is streamed as it is written,
// so the user is not staring at a spinner, and it still never becomes the answer.
// The browser demotes it to a run note when the tool call follows it; the
// runtime's answer stays the text of the turn that had no tool calls.
func TestToolCallTurnTextIsStreamedButIsNotTheAnswer(t *testing.T) {
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
	if got := assistantDeltaTexts(sink.events); strings.Join(got, ",") != "transient,final answer" {
		t.Fatalf("assistant deltas=%q", got)
	}
}

func newNonStreamingTestRunner(t *testing.T, m model.ToolCallingChatModel, invoker Invoker) *Runner {
	t.Helper()
	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:          "luna-test",
		Instruction:   instruction,
		Model:         m,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{NewTextTransformTool(invoker), NewReadFileTool(&recordingReader{}), NewListDirTool(&recordingReader{})}}},
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

// The core registers the three process-backed wrappers itself and adds one
// wrapper per contributed tool. With no capability registry configured that is
// exactly three tools, and nothing else. This Eino version passes the tool list
// to the model as a model option rather than calling this method, so the live
// observation of the offered set lives in
// TestTheModelSeesProcessToolsAndContributedTools; this stays as the contract
// for a version that does call it.
func (fakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	if len(names) != 3 || names[0] != ToolName || names[1] != ReadFileToolName || names[2] != ListDirToolName {
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

// The listing wrapper is the twin of the read wrapper: the call is announced,
// the serving generation stays in the event stream, and only the rendered list
// reaches the model.
func TestListDirToolEmitsStartedThenFinishedAndHidesIdentityFromTheModel(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	lister := &recordingReader{listOut: pluginhost.Output{Result: "2 entries: 1 dir, 1 file\n", Generation: 9, Version: "v1", PluginPID: 77}}
	got, err := NewListDirTool(lister).InvokableRun(ctx, `{"path":"docs"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2 entries: 1 dir, 1 file\n" {
		t.Fatalf("model-visible tool output must be the rendered listing, got %q", got)
	}
	// The wrapper forwards the raw requested path; resolving it is the host's
	// job, never the wrapper's and never the plugin's.
	if len(lister.listRequests) != 1 || lister.listRequests[0].Path != "docs" {
		t.Fatalf("wrapper requests=%+v", lister.listRequests)
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
	if !ok || started.Name != ListDirToolName {
		t.Fatalf("started payload: %#v", sink.events[0].Data)
	}
	args, ok := started.Arguments.(map[string]any)
	if !ok || args["path"] != "docs" {
		t.Fatalf("started arguments must carry the requested path: %#v", started.Arguments)
	}
	finished, ok := sink.events[1].Data.(ToolFinished)
	if !ok || finished.Name != ListDirToolName {
		t.Fatalf("finished payload: %#v", sink.events[1].Data)
	}
	if finished.Result != got || finished.Generation != 9 || finished.Version != "v1" || finished.PluginPID != 77 {
		t.Fatalf("plugin identity must stay exact in the UI event: %+v", finished)
	}
}

// A listing takes one path and nothing else. The recursion a model will reach
// for first — a depth — is refused before the host sees it, because one listing
// is one level by design.
func TestListDirToolRejectsMalformedOrWideningArgumentsBeforeTheHost(t *testing.T) {
	for _, arguments := range []string{`{}`, `{"path":""}`, `{"path":"."}{"path":"."}`, `{"path":".","depth":2}`, `{"path":".","recursive":true}`, `{"path":".","glob":"**/*.go"}`, `not json`, ``} {
		sink := &collectingSink{}
		ctx := WithRun(context.Background(), "run-1", sink)
		lister := &recordingReader{listOut: pluginhost.Output{Result: "should not be reached"}}
		got, err := NewListDirTool(lister).InvokableRun(ctx, arguments)
		if err != nil {
			t.Fatalf("arguments %q became a run error instead of a refusal: %v", arguments, err)
		}
		if !strings.HasPrefix(got, refusalPrefix) {
			t.Fatalf("arguments %q refusal = %q", arguments, got)
		}
		if len(lister.listRequests) != 0 {
			t.Fatalf("arguments %q reached the host: %+v", arguments, lister.listRequests)
		}
		if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.failed" {
			t.Fatalf("arguments %q events=%+v", arguments, sink.events)
		}
		failed := sink.events[1].Data.(ToolFailed)
		if failed.Name != ListDirToolName || failed.Error == "" {
			t.Fatalf("arguments %q failed payload=%+v", arguments, failed)
		}
	}
}

func TestListDirSchemaIsStrictAndOffersNoRecursion(t *testing.T) {
	info, err := NewListDirTool(&recordingReader{}).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != ListDirToolName {
		t.Fatalf("tool name = %q", info.Name)
	}
	if !strings.Contains(info.Desc, "one level") || !strings.Contains(info.Desc, "recursion") {
		t.Fatalf("the description must say that one call is one level and that there is no recursion option: %q", info.Desc)
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
		t.Fatalf("the schema required list must be exactly the path: %s", b)
	}
	properties, _ := raw["properties"].(map[string]any)
	if len(properties) != 1 {
		t.Fatalf("the listing offers more than a path: %s", b)
	}
}

// A refusal by the host (here: a path that is not a directory) is handed to the
// model as the tool's result, so the run continues and the user gets an
// explanation instead of a failed run.
func TestListDirToolHandsAHostRefusalToTheModel(t *testing.T) {
	sink := &collectingSink{}
	ctx := WithRun(context.Background(), "run-1", sink)
	lister := &recordingReader{listErr: fmt.Errorf("%w: %q", fileread.ErrNotDir, "go.mod")}
	got, err := NewListDirTool(lister).InvokableRun(ctx, `{"path":"go.mod"}`)
	if err != nil {
		t.Fatalf("a refused listing must not become a run error: %v", err)
	}
	if !strings.HasPrefix(got, refusalPrefix) || !strings.Contains(got, fileread.ErrNotDir.Error()) {
		t.Fatalf("model-visible refusal = %q", got)
	}
	if !strings.Contains(got, "go.mod") {
		t.Fatalf("the refusal must name the path the model asked for: %q", got)
	}
	if len(sink.events) != 2 || sink.events[0].Type != "tool.started" || sink.events[1].Type != "tool.failed" {
		t.Fatalf("events=%+v", sink.events)
	}
	failed := sink.events[1].Data.(ToolFailed)
	if failed.Name != ListDirToolName || failed.Error == "" {
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
		if _, err := NewListDirTool(&recordingReader{listErr: infra}).InvokableRun(ctx, `{"path":"."}`); !errors.Is(err, infra) {
			t.Fatalf("list_dir error = %v, want %v to stay fatal", err, infra)
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
	if finished.Name != ReadFileToolName {
		t.Fatalf("finished=%+v", finished)
	}
	// 同样与磁盘上的文件比对：这条断言要证明的是整轮读回了那个文件，
	// 不应该因为它被译成另一种语言而失败。
	onDisk, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(finished.Result, string(onDisk)) {
		t.Fatalf("finished.Result differs from the file on disk=%+v", finished)
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

// listModel calls the listing tool once and then answers, recording the tool
// result the model actually saw so a test can assert on the listing text rather
// than on the model's prose.
type listModel struct {
	path       string
	toolResult string
}

func (m *listModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *listModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	for _, msg := range input {
		if msg.Role == schema.Tool {
			m.toolResult = msg.Content
			return schema.AssistantMessage("Listed the directory.", nil), nil
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ListDirToolName, Arguments: fmt.Sprintf(`{"path":%q}`, m.path)}}}), nil
}
func (m *listModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// The listing tool runs through the real subprocess plugin behind Eino, against
// the repository root, and the model receives the rendered listing: the entries
// of the read root, with a kind and a size each.
func TestFakeModelListsTheReadRootThroughEinoEndToEnd(t *testing.T) {
	root, _ := filepath.Abs("../..")
	h, err := pluginhost.New(context.Background(), root, pluginhost.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	m := &listModel{path: "."}
	r, err := NewRunner(context.Background(), m, h, h)
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	answer, err := r.Run(context.Background(), RunRequest{Message: "what is in this project", RunID: "run-list", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Listed the directory." {
		t.Fatalf("answer=%q", answer)
	}
	types := []string{}
	for _, e := range sink.events {
		types = append(types, e.Type)
	}
	want := []string{"run.started", "tool.started", "tool.finished", "assistant.delta", "run.finished"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v", types)
	}
	started := sink.events[1].Data.(ToolStarted)
	if started.Name != ListDirToolName {
		t.Fatalf("started=%+v", started)
	}
	finished := sink.events[2].Data.(ToolFinished)
	if finished.Name != ListDirToolName {
		t.Fatalf("finished=%+v", finished)
	}
	if finished.Generation != 1 || finished.Version != "v1" || finished.PluginPID <= 0 {
		t.Fatalf("finished identity=%+v", finished)
	}
	if m.toolResult != finished.Result {
		t.Fatalf("the model did not receive the listing the tool produced: model=%q tool=%q", m.toolResult, finished.Result)
	}
	// The read root really does hold these, and the listing has to name each one
	// with its kind: the point of the tool is that a model can tell a directory
	// from a file without reading anything, and the whole point of the tool is
	// that this happens on real repository contents.
	lines := strings.Split(strings.TrimRight(finished.Result, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("the listing has no entries:\n%s", finished.Result)
	}
	kinds := map[string]string{}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			kinds[fields[len(fields)-1]] = fields[0]
		}
	}
	for name, wantKind := range map[string]string{"cmd": "dir", "internal": "dir", "docs": "dir", "go.mod": "file", "README.md": "file"} {
		if kinds[name] != wantKind {
			t.Fatalf("%q was listed as %q, want %q:\n%s", name, kinds[name], wantKind, finished.Result)
		}
	}
	// The listing is not a recursive dump: it reports the entries of the root
	// itself, so a path that only exists below it must not appear.
	if strings.Contains(finished.Result, "internal/agent") || strings.Contains(finished.Result, "app.js") {
		t.Fatalf("the listing went below one level:\n%s", finished.Result)
	}
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

// TestTerminalEventSaysWhyTheRunEnded pins the one place that decides which event
// ends a run. A stopped run is not a broken one, and the reason has to separate a
// caller pressing Stop from the run's own deadline.
func TestTerminalEventSaysWhyTheRunEnded(t *testing.T) {
	stopped, stop := context.WithCancelCause(context.Background())
	stop(errors.New("stopped by the caller"))
	defer stop(nil)
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()

	tests := []struct {
		name       string
		ctx        context.Context
		err        error
		wantType   string
		wantReason string
	}{
		{"answered", context.Background(), nil, "run.finished", ""},
		{"stopped by the caller", stopped, context.Canceled, "run.cancelled", CancelReasonUser},
		{"stopped, surfaced as some other error", stopped, errors.New("stream closed"), "run.cancelled", CancelReasonUser},
		{"stopped by the deadline", expired, context.DeadlineExceeded, "run.cancelled", CancelReasonTimeout},
		{"broken", context.Background(), errors.New("model unavailable"), "run.failed", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := TerminalEvent(tc.ctx, "run-1", "the answer", tc.err)
			if event.Type != tc.wantType {
				t.Fatalf("type=%s want=%s", event.Type, tc.wantType)
			}
			if !IsTerminalEvent(event.Type) {
				t.Fatalf("%s is not a terminal event", event.Type)
			}
			if tc.wantReason == "" {
				return
			}
			cancelled, ok := event.Data.(RunCancelled)
			if !ok || cancelled.Reason != tc.wantReason || cancelled.RunID != "run-1" {
				t.Fatalf("data=%#v want reason=%s", event.Data, tc.wantReason)
			}
		})
	}
}

// blockingInvoker holds one call open until the run's context ends, so the test
// can stop a run while a tool is genuinely in flight.
type blockingInvoker struct {
	entered chan struct{}
	once    sync.Once
}

func (b *blockingInvoker) Invoke(ctx context.Context, _ pluginhost.Input) (pluginhost.Output, error) {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return pluginhost.Output{}, ctx.Err()
}

// transformCallModel always asks for the transform tool, which is enough to put a
// real call in flight.
type transformCallModel struct{}

func (transformCallModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return transformCallModel{}, nil
}
func (transformCallModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ToolName, Arguments: `{"text":"hi"}`}}}), nil
}
func (t transformCallModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := t.Generate(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// TestStoppingARunDuringAToolCallEndsTheCallAndTheRun covers what Stop has to
// handle when a tool is in flight: the call closes with its duration, the run
// ends as cancelled, and the model is never told the tool declined a call it did
// not decline.
func TestStoppingARunDuringAToolCallEndsTheCallAndTheRun(t *testing.T) {
	invoker := &blockingInvoker{entered: make(chan struct{})}
	r, err := NewRunner(context.Background(), transformCallModel{}, invoker, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	ctx, stop := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Run(ctx, RunRequest{Message: "transform this", RunID: "run-stop", Sink: sink})
	}()
	select {
	case <-invoker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool call never started")
	}
	stop(errors.New("stopped while the tool ran"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after the stop")
	}

	sink.mu.Lock()
	events := append([]Event{}, sink.events...)
	sink.mu.Unlock()
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	terminals := 0
	for _, e := range events {
		if IsTerminalEvent(e.Type) {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events=%d types=%v", terminals, types)
	}
	last := events[len(events)-1]
	cancelled, ok := last.Data.(RunCancelled)
	if last.Type != "run.cancelled" || !ok || cancelled.Reason != CancelReasonUser {
		t.Fatalf("terminal event=%#v types=%v", last, types)
	}
	failures := 0
	for _, e := range events {
		if e.Type == "run.finished" {
			t.Fatalf("a stopped run reported an answer: %v", types)
		}
		if e.Type != "tool.failed" {
			continue
		}
		failures++
		failed, ok := e.Data.(ToolFailed)
		if !ok {
			t.Fatalf("tool.failed data=%#v", e.Data)
		}
		if failed.DurationMS < 0 || failed.Name != ToolName {
			t.Fatalf("tool.failed=%+v", failed)
		}
		if strings.HasPrefix(failed.Error, refusalPrefix) {
			t.Fatalf("a stopped call was reported to the model as a refusal: %q", failed.Error)
		}
	}
	if failures != 1 {
		t.Fatalf("tool.failed events=%d types=%v", failures, types)
	}
}

// stagedModel streams chunks only when the test sends them, so a test can tell
// whether a delta reached the sink while the turn was still open. An unbuffered
// pipe makes each send wait for the reader, which is what keeps the order of the
// test's own observations meaningful.
type stagedModel struct {
	chunks  chan *schema.Message
	started chan struct{}
	once    sync.Once
}

func (m *stagedModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *stagedModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("Generate is not used when streaming")
}
func (m *stagedModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](0)
	go func() {
		defer sw.Close()
		m.once.Do(func() { close(m.started) })
		for chunk := range m.chunks {
			sw.Send(chunk, nil)
		}
	}()
	return sr, nil
}

// TestAnswerStreamsWhileTheTurnIsOpen is the regression guard for real streaming:
// the first delta has to reach the sink before the second chunk exists at all. A
// runtime that collects a whole turn and emits it afterwards passes every test
// that only checks for assistant.delta events, and fails this one.
func TestAnswerStreamsWhileTheTurnIsOpen(t *testing.T) {
	m := &stagedModel{chunks: make(chan *schema.Message), started: make(chan struct{})}
	r, err := NewRunner(context.Background(), m, &fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.Run(context.Background(), RunRequest{Message: "hi", RunID: "run-stream", Sink: sink}); err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	select {
	case <-m.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was never called")
	}
	waitForDelta := func() bool {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			sink.mu.Lock()
			found := false
			for _, e := range sink.events {
				if e.Type == "assistant.delta" {
					found = true
					break
				}
			}
			sink.mu.Unlock()
			if found {
				return true
			}
			time.Sleep(2 * time.Millisecond)
		}
		return false
	}

	m.chunks <- schema.AssistantMessage("hello ", nil)
	if !waitForDelta() {
		t.Fatal("the first delta only arrived after the turn ended: the answer is not streamed")
	}

	// The second chunk also carries usage, which the runtime reports only because
	// the provider reported it.
	last := schema.AssistantMessage("world", nil)
	last.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens:            11,
		CompletionTokens:        2,
		TotalTokens:             13,
		PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 5},
		CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 1},
	}}
	m.chunks <- last
	close(m.chunks)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never finished")
	}

	sink.mu.Lock()
	events := append([]Event{}, sink.events...)
	sink.mu.Unlock()
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	want := []string{"run.started", "assistant.delta", "assistant.delta", "usage.updated", "run.finished"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("types=%v want=%v", types, want)
	}
	if answer := events[len(events)-1].Data.(RunFinished).Answer; answer != "hello world" {
		t.Fatalf("answer=%q", answer)
	}
	usage, ok := events[3].Data.(UsageUpdated)
	if !ok {
		t.Fatalf("usage event data=%#v", events[3].Data)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 2 || usage.TotalTokens != 13 || usage.CachedTokens != 5 || usage.ReasoningTokens != 1 {
		t.Fatalf("usage=%+v", usage)
	}
}

// TestReasoningIsStreamedAndKeptOutOfTheAnswer pins both halves of the reasoning
// contract: what the provider exposes is streamed on its own event, in order
// before the turn's text, and none of it becomes the answer.
func TestReasoningIsStreamedAndKeptOutOfTheAnswer(t *testing.T) {
	m := &stagedModel{chunks: make(chan *schema.Message), started: make(chan struct{})}
	r, err := NewRunner(context.Background(), m, &fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.Run(context.Background(), RunRequest{Message: "hi", RunID: "run-think", Sink: sink}); err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	select {
	case <-m.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was never called")
	}
	thinking := schema.AssistantMessage("", nil)
	thinking.ReasoningContent = "先算一下…"
	m.chunks <- thinking
	m.chunks <- schema.AssistantMessage("答案是 42。", nil)
	close(m.chunks)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never finished")
	}

	sink.mu.Lock()
	events := append([]Event{}, sink.events...)
	sink.mu.Unlock()
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	want := []string{"run.started", "assistant.reasoning", "assistant.delta", "run.finished"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("types=%v want=%v", types, want)
	}
	if got := events[1].Data.(AssistantReasoning).Text; got != "先算一下…" {
		t.Fatalf("reasoning=%q", got)
	}
	if answer := events[len(events)-1].Data.(RunFinished).Answer; answer != "答案是 42。" {
		t.Fatalf("answer=%q", answer)
	}
}
