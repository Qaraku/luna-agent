package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	sessionstore "github.com/Qaraku/luna-agent/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// 只替换模型与无关的子进程工具；真实 Memory、工具包装、事件和会话落盘都参与运行。
type recallKernelModel struct {
	mu        sync.Mutex
	arguments string
	offered   []*schema.ToolInfo
	results   []string
}

func (m *recallKernelModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *recallKernelModel) respond(input []*schema.Message, options []model.Option) *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 当前 Eino 在每次模型调用的 options 中传入工具清单，而不是调用 WithTools。
	m.offered = model.GetCommonOptions(&model.Options{}, options...).Tools
	enabled := false
	for _, tool := range m.offered {
		if tool.Name == RecallToolName {
			enabled = true
		}
	}
	if !enabled {
		return schema.AssistantMessage("memory disabled", nil)
	}
	for i := len(input) - 1; i >= 0; i-- {
		if input[i].Role == schema.Tool {
			m.results = append(m.results, input[i].Content)
			return schema.AssistantMessage("checked", nil)
		}
	}
	index := 0
	return schema.AssistantMessage("", []schema.ToolCall{{Index: &index, ID: "recall-1", Type: "function", Function: schema.FunctionCall{Name: RecallToolName, Arguments: m.arguments}}})
}

func (m *recallKernelModel) Generate(_ context.Context, input []*schema.Message, options ...model.Option) (*schema.Message, error) {
	return m.respond(input, options), nil
}

func (m *recallKernelModel) Stream(_ context.Context, input []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.respond(input, options)}), nil
}

func (m *recallKernelModel) snapshot() ([]*schema.ToolInfo, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*schema.ToolInfo(nil), m.offered...), append([]string(nil), m.results...)
}

type recallKernelProcesses struct{}

func (recallKernelProcesses) Invoke(context.Context, pluginhost.Input) (pluginhost.Output, error) {
	return pluginhost.Output{}, errors.New("unexpected process tool call")
}
func (recallKernelProcesses) ReadFile(context.Context, pluginhost.ReadRequest) (pluginhost.Output, error) {
	return pluginhost.Output{}, errors.New("unexpected file read")
}
func (recallKernelProcesses) ListDir(context.Context, pluginhost.ListRequest) (pluginhost.Output, error) {
	return pluginhost.Output{}, errors.New("unexpected directory listing")
}

type recallKernelSink struct {
	mu     sync.Mutex
	events []agent.Event
}

func (s *recallKernelSink) Emit(event agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func TestRecallQueryThroughKernelAndSessionTranscript(t *testing.T) {
	for _, tc := range []struct {
		name, arguments, want string
		refused               bool
	}{
		{"older fact", `{"query":"乌龙茶"}`, "旧偏好：乌龙茶", false},
		{"page", `{"query":"recent","offset":5,"limit":2}`, "recent 53", false},
		{"refused limit", `{"query":"recent","limit":0}`, "the tool refused this call: ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capability, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
			for i := 0; i < 60; i++ {
				text := fmt.Sprintf("recent %02d", i)
				if i == 0 {
					text = "旧偏好：乌龙茶"
				}
				if _, err := capability.store.Remember("old-session", text, at.Add(time.Duration(i)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			before := storedBytes(t, capability.store)
			registry := plugin.NewRegistry(plugin.PermissionStateWrite)
			if err := registry.Register(capability); err != nil {
				t.Fatal(err)
			}
			if err := registry.Enable(PluginID); err != nil {
				t.Fatal(err)
			}
			sessions, err := sessionstore.Open(filepath.Join(t.TempDir(), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			sessionID, err := sessions.Create("recall integration")
			if err != nil {
				t.Fatal(err)
			}
			fake := &recallKernelModel{arguments: tc.arguments}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runner, err := agent.NewRunner(ctx, fake, recallKernelProcesses{}, recallKernelProcesses{}, agent.WithCapabilities(registry), agent.WithHistory(sessions), agent.WithTranscript(sessions))
			if err != nil {
				t.Fatal(err)
			}
			sink := &recallKernelSink{}
			answer, err := runner.Run(ctx, agent.RunRequest{RunID: "recall-run", SessionID: sessionID, Message: "look up the memory", Sink: sink})
			if err != nil || answer != "checked" {
				t.Fatalf("run should finish even for a refused call: %q, %v", answer, err)
			}
			offered, results := fake.snapshot()
			if len(results) != 1 || !strings.Contains(results[0], tc.want) {
				t.Fatalf("model did not receive the expected result: %v", results)
			}
			found := false
			for _, tool := range offered {
				if tool.Name != RecallToolName {
					continue
				}
				found = true
				shape, err := tool.ParamsOneOf.ToJSONSchema()
				if err != nil || shape.Properties.Len() != 5 || len(shape.Required) != 0 {
					t.Fatalf("model did not receive the optional query schema: %v, %v", shape, err)
				}
			}
			if !found {
				t.Fatal("recall is missing from the enabled model tool set")
			}
			sink.mu.Lock()
			events := append([]agent.Event(nil), sink.events...)
			sink.mu.Unlock()
			wantEvent := "tool.finished"
			if tc.refused {
				wantEvent = "tool.failed"
			}
			callEvents, terminalEvents := 0, 0
			for _, event := range events {
				if event.Type == wantEvent {
					callEvents++
				}
				if agent.IsTerminalEvent(event.Type) {
					terminalEvents++
					if event.Type != "run.finished" {
						t.Errorf("refused call failed the run: %s", event.Type)
					}
				}
			}
			if callEvents != 1 || terminalEvents != 1 {
				t.Fatalf("expected one %s and one run.finished: %+v", wantEvent, events)
			}
			transcript, err := sessions.Read(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			var decodedArgs any
			if err := json.Unmarshal([]byte(tc.arguments), &decodedArgs); err != nil {
				t.Fatal(err)
			}
			wantArguments := recallJSON(t, decodedArgs)
			calls := 0
			for _, record := range transcript.Records {
				if record.ToolCall != nil {
					calls++
					call := record.ToolCall
					if call.Name != RecallToolName || (call.Error != "") != tc.refused || call.Arguments != wantArguments {
						t.Fatalf("incorrect recorded call: %+v", call)
					}
				}
			}
			if calls != 1 {
				t.Fatalf("recorded calls: %d, want 1", calls)
			}
			if err := registry.Disable(PluginID); err != nil {
				t.Fatal(err)
			}
			answer, err = runner.Run(ctx, agent.RunRequest{RunID: "disabled-run", SessionID: sessionID, Message: "check again", Sink: &recallKernelSink{}})
			if err != nil || answer != "memory disabled" {

				t.Fatalf("disabled run: %q, %v", answer, err)
			}
			offered, _ = fake.snapshot()
			for _, tool := range offered {
				if tool.Name == RecallToolName || tool.Name == RememberToolName {
					t.Fatalf("disabled memory still offered %s", tool.Name)
				}
			}
			if string(before) != string(storedBytes(t, capability.store)) {
				t.Fatal("recall or disabling memory changed its data")
			}
		})
	}
}
