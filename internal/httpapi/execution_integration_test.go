package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/terminal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type executionModel struct {
	command          string
	systems, results *[]string
}

func (m executionModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m executionModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	for _, message := range input {
		if message.Role == schema.System {
			*m.systems = append(*m.systems, message.Content)
		}
	}
	for _, message := range input {
		if message.Role == schema.Tool {
			*m.results = append(*m.results, message.Content)
			return schema.AssistantMessage("done", nil), nil
		}
	}
	body, _ := json.Marshal(map[string]string{"command": m.command})
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "exec-test", Type: "function", Function: schema.FunctionCall{Name: terminal.RunToolName, Arguments: string(body)}}}), nil
}
func (m executionModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestConfirmedExecutionGrantReachesActualTerminalAndModelContext(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("native integration")
	root, outside := t.TempDir(), t.TempDir()
	marker := filepath.Join(outside, "result")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	var systems, results []string
	model := executionModel{command: "printf approved > " + quoted + "; cat " + quoted, systems: &systems, results: &results}
	reg := plugin.NewRegistry(plugin.PermissionProcessExec, plugin.PermissionFilesystemWrite, plugin.PermissionNetworkFetch)
	if err := reg.Register(terminal.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enable(terminal.PluginID); err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(context.Background(), model, nil, nil, agent.WithCapabilities(reg), agent.WithHistory(sessions), agent.WithTranscript(sessions))
	if err != nil {
		t.Fatal(err)
	}
	h := New(&fakePlugins{state: everyAllowlistedTool()}, runner, sessions, Info{BoundHost: "127.0.0.1:43210"}, WithCapabilities(reg), WithFallbackRoot(root))
	if got := controlsRequest(t, h, "/api/sessions/"+id+"/execution", map[string]any{"mode": "full_access", "confirm_full_access": true}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	response := controlsRequest(t, h, "/api/runs", map[string]string{"session_id": id, "message": "run the owned test command"})
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: run.finished") {
		t.Fatalf("run=%d %s", response.Code, response.Body)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "approved" {
		t.Fatalf("native command did not write the owned outside file: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0], "execution mode: full_access") {
		t.Fatalf("tool result=%v", results)
	}
	if len(systems) == 0 || !strings.Contains(systems[0], "Terminal execution mode: full_access") {
		t.Fatal("model was not told its actual execution policy")
	}
}
