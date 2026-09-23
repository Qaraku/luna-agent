package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// The store is both ports of the durable side, with no adapter in between.
func TestStoreSatisfiesTheAgentPorts(t *testing.T) {
	var _ History = (*store.Store)(nil)
	var _ Transcript = (*store.Store)(nil)
}

type fakeHistory struct {
	records   []store.MessageRecord
	err       error
	requested []string
}

func (h *fakeHistory) Messages(sessionID string) ([]store.MessageRecord, error) {
	h.requested = append(h.requested, sessionID)
	if h.err != nil {
		return nil, h.err
	}
	return h.records, nil
}

// fakeTranscript records what the run persisted, and can fail a chosen append so
// the failure path is testable.
type fakeTranscript struct {
	mu        sync.Mutex
	failOn    string
	sessionID string
	messages  []store.MessageRecord
	toolCalls []store.ToolCallRecord
	runs      []store.RunRecord
}

func (t *fakeTranscript) AppendMessage(sessionID string, record store.MessageRecord) error {
	if t.failOn == "message" {
		return errors.New("transcript unavailable")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = sessionID
	t.messages = append(t.messages, record)
	return nil
}

func (t *fakeTranscript) AppendToolCall(sessionID string, record store.ToolCallRecord) error {
	if t.failOn == "tool_call" {
		return errors.New("transcript unavailable")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = sessionID
	t.toolCalls = append(t.toolCalls, record)
	return nil
}

func (t *fakeTranscript) AppendRun(sessionID string, record store.RunRecord) error {
	if t.failOn == "run" {
		return errors.New("transcript unavailable")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = sessionID
	t.runs = append(t.runs, record)
	return nil
}

func (t *fakeTranscript) snapshot() ([]store.MessageRecord, []store.ToolCallRecord, []store.RunRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]store.MessageRecord{}, t.messages...), append([]store.ToolCallRecord{}, t.toolCalls...), append([]store.RunRecord{}, t.runs...)
}

// captureModel records every input the agent hands the model, which is what
// "assemble the model input" has to be proven on.
type captureModel struct {
	mu      sync.Mutex
	inputs  [][]*schema.Message
	replies []*schema.Message
	answer  string
}

func (m *captureModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *captureModel) next(input []*schema.Message) *schema.Message {
	m.mu.Lock()
	m.inputs = append(m.inputs, append([]*schema.Message{}, input...))
	m.mu.Unlock()
	for _, message := range input {
		if message.Role == schema.Tool {
			return schema.AssistantMessage(m.answer, nil)
		}
	}
	if len(m.replies) == 0 {
		return schema.AssistantMessage(m.answer, nil)
	}
	reply := m.replies[0]
	m.replies = m.replies[1:]
	return reply
}

func (m *captureModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.next(input), nil
}

func (m *captureModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next(input)}), nil
}

func (m *captureModel) first() []*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputs) == 0 {
		return nil
	}
	return m.inputs[0]
}

func (m *captureModel) all() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*schema.Message{}, m.inputs...)
}

func historyOf(pairs ...string) *fakeHistory {
	records := make([]store.MessageRecord, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		records = append(records, store.MessageRecord{Type: store.TypeMessage, RunID: "run-old", Role: pairs[i], Text: pairs[i+1], At: time.Unix(int64(i), 0)})
	}
	return &fakeHistory{records: records}
}

// The run's model input is the system prompt, then the session's history in
// order, then this turn's user message — assembled from disk, not from memory.
func TestModelInputIsSystemPromptPlusHistoryPlusTheNewMessage(t *testing.T) {
	m := &captureModel{answer: "third answer"}
	history := historyOf(store.RoleUser, "first question", store.RoleAssistant, "first answer")
	runner, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithHistory(history))
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	if _, err := runner.Run(context.Background(), RunRequest{Message: "third question", RunID: "run-1", SessionID: "session-1", Sink: sink}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(history.requested) != 1 || history.requested[0] != "session-1" {
		t.Fatalf("history was not read for the session: %v", history.requested)
	}
	input := m.first()
	if len(input) != 4 {
		t.Fatalf("model input=%v, want system + two history messages + this turn", contents(input))
	}
	wantRoles := []schema.RoleType{schema.System, schema.User, schema.Assistant, schema.User}
	wantText := []string{instruction, "first question", "first answer", "third question"}
	for i := range wantRoles {
		if input[i].Role != wantRoles[i] || input[i].Content != wantText[i] {
			t.Fatalf("input[%d]=%s/%q, want %s/%q", i, input[i].Role, input[i].Content, wantRoles[i], wantText[i])
		}
	}
}

func contents(messages []*schema.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, fmt.Sprintf("%s:%s", message.Role, message.Content))
	}
	return out
}

// The cap is on the count of prior messages, and it is a contiguous suffix: the
// oldest messages are dropped, the newest are kept, and order is preserved.
func TestHistoryCapKeepsTheNewestMessagesUpToTheCount(t *testing.T) {
	// The boundary: exactly the cap is kept whole.
	atCap := make([]store.MessageRecord, 0, MaxHistoryMessages)
	for i := 0; i < MaxHistoryMessages; i++ {
		atCap = append(atCap, store.MessageRecord{Role: store.RoleUser, Text: fmt.Sprintf("m%02d", i)})
	}
	kept := selectHistory(atCap)
	if len(kept) != MaxHistoryMessages || kept[0].Text != "m00" || kept[MaxHistoryMessages-1].Text != fmt.Sprintf("m%02d", MaxHistoryMessages-1) {
		t.Fatalf("the boundary dropped messages: %d kept", len(kept))
	}

	// One over the cap: the single oldest message goes.
	over := append([]store.MessageRecord{{Role: store.RoleUser, Text: "oldest"}}, atCap...)
	kept = selectHistory(over)
	if len(kept) != MaxHistoryMessages {
		t.Fatalf("kept=%d, want %d", len(kept), MaxHistoryMessages)
	}
	if kept[0].Text != "m00" {
		t.Fatalf("dropping must start at the oldest message: kept[0]=%q", kept[0].Text)
	}
	if kept[len(kept)-1].Text != fmt.Sprintf("m%02d", MaxHistoryMessages-1) {
		t.Fatalf("the newest message was dropped: %q", kept[len(kept)-1].Text)
	}

	// Three over the cap: the three oldest go, in order.
	over = append([]store.MessageRecord{{Role: store.RoleUser, Text: "a"}, {Role: store.RoleUser, Text: "b"}, {Role: store.RoleUser, Text: "c"}}, atCap...)
	kept = selectHistory(over)
	if len(kept) != MaxHistoryMessages || kept[0].Text != "m00" || kept[len(kept)-1].Text != fmt.Sprintf("m%02d", MaxHistoryMessages-1) {
		t.Fatalf("kept=%d first=%q last=%q", len(kept), kept[0].Text, kept[len(kept)-1].Text)
	}
	for i := 1; i < len(kept); i++ {
		if kept[i-1].Text >= kept[i].Text {
			t.Fatalf("history order changed at %d: %q then %q", i, kept[i-1].Text, kept[i].Text)
		}
	}
}

// The cap is also on bytes, with the same policy: a message that would exceed it
// stops the scan, so everything older goes with it.
func TestHistoryCapDropsOldestFirstByBytes(t *testing.T) {
	small := store.MessageRecord{Role: store.RoleUser, Text: strings.Repeat("s", 100)}
	newest := store.MessageRecord{Role: store.RoleAssistant, Text: strings.Repeat("n", 100)}

	// A single old message larger than the cap is dropped, and the newer one is
	// kept.
	huge := store.MessageRecord{Role: store.RoleUser, Text: strings.Repeat("h", MaxHistoryBytes+1)}
	kept := selectHistory([]store.MessageRecord{huge, newest})
	if len(kept) != 1 || kept[0].Text != newest.Text {
		t.Fatalf("kept=%d, want only the newest message", len(kept))
	}

	// The kept set stays a contiguous suffix: an old message is never kept
	// behind a dropped one.
	filler := store.MessageRecord{Role: store.RoleAssistant, Text: strings.Repeat("f", MaxHistoryBytes-len(small.Text)-len(newest.Text))}
	exact := []store.MessageRecord{small, filler, newest}
	kept = selectHistory(exact)
	if len(kept) != 3 {
		t.Fatalf("a history that fits the byte cap was trimmed: %d", len(kept))
	}
	total := 0
	for _, message := range kept {
		total += len(message.Text)
	}
	if total != MaxHistoryBytes {
		t.Fatalf("total=%d, want %d", total, MaxHistoryBytes)
	}
	// One byte more than the cap: the oldest message is dropped, not the filler.
	over := []store.MessageRecord{store.MessageRecord{Role: store.RoleUser, Text: small.Text + "x"}, filler, newest}
	kept = selectHistory(over)
	if len(kept) != 2 || kept[0].Text != filler.Text || kept[1].Text != newest.Text {
		t.Fatalf("kept=%v, want the newest two", contentsOf(kept))
	}
}

func contentsOf(messages []store.MessageRecord) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, fmt.Sprintf("%s:%d", message.Role, len(message.Text)))
	}
	return out
}

// The assembly drops this run's own records and never guesses at an unknown
// role, and the cap reaches the model through the same code path.
func TestHistoryAssemblyExcludesThisRunAndUnknownRoles(t *testing.T) {
	history := &fakeHistory{records: []store.MessageRecord{
		{Role: store.RoleUser, Text: "kept", RunID: "run-old"},
		{Role: store.RoleAssistant, Text: "this turn already persisted", RunID: "run-new"},
		{Role: "system", Text: "not a session role", RunID: "run-old"},
		{Role: store.RoleUser, Text: "also kept", RunID: "run-old"},
	}}
	// The count cap is proven through the same call the runner makes.
	m := &captureModel{answer: "answer"}
	runner, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithHistory(history))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), RunRequest{Message: "next", RunID: "run-new", SessionID: "s", Sink: &collectingSink{}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	input := m.first()
	if len(input) != 4 {
		t.Fatalf("input=%v", contents(input))
	}
	if input[1].Content != "kept" || input[2].Content != "also kept" || input[3].Content != "next" {
		t.Fatalf("history=%v", contents(input))
	}
	for _, message := range input {
		if strings.Contains(message.Content, "this turn already persisted") || strings.Contains(message.Content, "not a session role") {
			t.Fatalf("input carried the current run or an unknown role: %v", contents(input))
		}
	}

	// The count cap also holds when the model is reached through a real Runner.
	many := make([]store.MessageRecord, 0, MaxHistoryMessages+5)
	for i := 0; i < MaxHistoryMessages+5; i++ {
		many = append(many, store.MessageRecord{Role: store.RoleUser, Text: fmt.Sprintf("m%03d", i), RunID: "run-old"})
	}
	limited := &captureModel{answer: "answer"}
	runner, err = NewRunner(context.Background(), limited, fakeInvoker{}, &recordingReader{}, WithHistory(&fakeHistory{records: many}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), RunRequest{Message: "next", RunID: "run-new", SessionID: "s", Sink: &collectingSink{}}); err != nil {
		t.Fatalf("run: %v", err)
	}
	input = limited.first()
	if len(input) != MaxHistoryMessages+2 {
		t.Fatalf("input has %d messages, want the system prompt, the capped history and the new message", len(input))
	}
	if input[1].Content != fmt.Sprintf("m%03d", 5) {
		t.Fatalf("the oldest messages were not dropped: first history message=%q", input[1].Content)
	}
	if input[len(input)-1].Content != "next" {
		t.Fatalf("last input=%q", input[len(input)-1].Content)
	}
}

// The transcript gets the run, and the model context carries no plugin identity.
func TestTranscriptRecordsTheRunWithoutPluginIdentity(t *testing.T) {
	index := 0
	m := &captureModel{
		replies: []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ToolName, Arguments: `{"text":"hello"}`}}}}},
		answer:  "final answer",
	}
	transcript := &fakeTranscript{}
	invoker := fakeInvoker{out: pluginhost.Output{Result: "trimmed", Generation: 7, Version: "v9-weird", PluginPID: 4242}}
	runner, err := NewRunner(context.Background(), m, invoker, &recordingReader{}, WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	if _, err := runner.Run(context.Background(), RunRequest{Message: "transform hello", RunID: "run-1", SessionID: "session-1", Sink: sink}); err != nil {
		t.Fatalf("run: %v", err)
	}

	messages, toolCalls, runs := transcript.snapshot()
	if transcript.sessionID != "session-1" {
		t.Fatalf("transcript session=%q", transcript.sessionID)
	}
	if len(messages) != 2 || messages[0].Role != store.RoleUser || messages[0].Text != "transform hello" || messages[0].RunID != "run-1" {
		t.Fatalf("messages=%+v", messages)
	}
	if messages[1].Role != store.RoleAssistant || messages[1].Text != "final answer" {
		t.Fatalf("assistant message=%+v", messages[1])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls=%+v", toolCalls)
	}
	if call := toolCalls[0]; call.Name != ToolName || call.Arguments != `{"text":"hello"}` || call.Result != "trimmed" || call.Error != "" || call.RunID != "run-1" {
		t.Fatalf("tool call=%+v", call)
	}
	if len(runs) != 1 || runs[0].Status != store.StatusOK || runs[0].RunID != "run-1" || runs[0].EndedAt.Before(runs[0].StartedAt) {
		t.Fatalf("runs=%+v", runs)
	}

	// The run.started event still carries the session id, and the run's events
	// are unchanged by persistence.
	if started, ok := sink.events[0].Data.(RunStarted); !ok || started.SessionID != "session-1" || started.RunID != "run-1" {
		t.Fatalf("run.started=%#v", sink.events[0].Data)
	}
	types := make([]string, 0, len(sink.events))
	for _, event := range sink.events {
		types = append(types, event.Type)
	}
	want := []string{"run.started", "tool.started", "tool.finished", "assistant.delta", "run.finished"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v", types)
	}

	// No plugin identity in anything the model saw.
	for _, input := range m.all() {
		for _, message := range input {
			for _, leak := range []string{"4242", "v9-weird", "generation", "plugin_pid", "Generation"} {
				if strings.Contains(message.Content, leak) {
					t.Fatalf("the model context leaked plugin identity %q: %v", leak, contents(input))
				}
			}
		}
	}
}

// A transcript that cannot be written fails the run instead of reporting a
// success that would not survive a restart.
func TestTranscriptFailureFailsTheRun(t *testing.T) {
	m := &captureModel{answer: "answer"}
	runner, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithTranscript(&fakeTranscript{failOn: "message"}))
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	_, err = runner.Run(context.Background(), RunRequest{Message: "hello", RunID: "run-1", SessionID: "session-1", Sink: sink})
	if err == nil || !strings.Contains(err.Error(), "persist user message") {
		t.Fatalf("err=%v", err)
	}
	if len(m.all()) != 0 {
		t.Fatalf("the model was called for an unrecorded run: %v", contents(m.first()))
	}
	last := sink.events[len(sink.events)-1]
	if last.Type != "run.failed" {
		t.Fatalf("terminal event=%s", last.Type)
	}
	if !strings.Contains(last.Data.(RunFailed).Error, "persist user message") {
		t.Fatalf("terminal payload=%#v", last.Data)
	}
}

// A tool call that cannot be persisted fails the run as well.
func TestToolCallPersistenceFailureFailsTheRun(t *testing.T) {
	index := 0
	m := &captureModel{
		replies: []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{Index: &index, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: ToolName, Arguments: `{"text":"hello"}`}}}}},
		answer:  "final answer",
	}
	runner, err := NewRunner(context.Background(), m, fakeInvoker{out: pluginhost.Output{Result: "trimmed"}}, &recordingReader{}, WithTranscript(&fakeTranscript{failOn: "tool_call"}))
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	_, err = runner.Run(context.Background(), RunRequest{Message: "hello", RunID: "run-1", SessionID: "session-1", Sink: sink})
	if err == nil || !strings.Contains(err.Error(), "persist tool call") {
		t.Fatalf("err=%v", err)
	}
	last := sink.events[len(sink.events)-1]
	if last.Type != "run.failed" || !strings.Contains(last.Data.(RunFailed).Error, "tool call") {
		t.Fatalf("terminal event=%#v", last)
	}
}

// A run whose history cannot be read fails before the model is called.
func TestHistoryReadFailureFailsTheRun(t *testing.T) {
	m := &captureModel{answer: "answer"}
	runner, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{}, WithHistory(&fakeHistory{err: store.ErrNotFound}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), RunRequest{Message: "hello", RunID: "run-1", SessionID: "missing", Sink: &collectingSink{}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	if len(m.all()) != 0 {
		t.Fatal("the model was called without history")
	}
}

func TestRunStatusVocabulary(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, store.StatusOK},
		{"cancelled", context.Canceled, store.StatusCancelled},
		{"deadline", context.DeadlineExceeded, store.StatusCancelled},
		{"wrapped cancellation", fmt.Errorf("run: %w", context.Canceled), store.StatusCancelled},
		{"failure", errors.New("model unavailable"), store.StatusError},
	}
	for _, tc := range cases {
		if got := runStatus(tc.err); got != tc.want {
			t.Fatalf("%s: status=%q, want %q", tc.name, got, tc.want)
		}
	}
}

// Persistence must not change the events the browser sees.
func TestRecorderForwardsEventsUnchanged(t *testing.T) {
	inner := &collectingSink{}
	recorder := newRecorder(inner, &fakeTranscript{}, "session-1", "run-1")
	for _, typ := range []string{"run.started", "assistant.delta", "run.finished"} {
		recorder.Emit(Event{Type: typ})
	}
	if len(inner.events) != 3 || inner.events[0].Type != "run.started" || inner.events[2].Type != "run.finished" {
		t.Fatalf("events=%+v", inner.events)
	}
	if err := recorder.Err(); err != nil {
		t.Fatalf("err=%v", err)
	}
	// A malformed payload is forwarded, not persisted, and never panics.
	recorder.Emit(Event{Type: "tool.started", Data: "not a payload"})
	recorder.Emit(Event{Type: "tool.finished", Data: 42})
	if err := recorder.Err(); err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(inner.events) != 5 {
		t.Fatalf("events=%+v", inner.events)
	}
	_, toolCalls, _ := (&fakeTranscript{}).snapshot()
	if len(toolCalls) != 0 {
		t.Fatalf("tool calls=%+v", toolCalls)
	}
}

// The tool wrapper interface is still satisfied: persistence is not a new tool.
func TestRecorderIsNotATool(t *testing.T) {
	var _ tool.BaseTool = (*TextTransformTool)(nil)
	var _ tool.InvokableTool = (*ReadFileTool)(nil)
}
