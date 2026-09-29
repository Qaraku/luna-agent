package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/plugins/jsonformat"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type jsonCapabilityModel struct {
	enabled bool
	results *[]string
}

func (m jsonCapabilityModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.enabled = false
	for _, tool := range tools {
		if tool.Name == jsonformat.ToolName {
			m.enabled = true
		}
	}
	return m, nil
}
func (m jsonCapabilityModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	// 当前 Eino 通过每次调用的 model.Option 传工具列表，不一定调用 WithTools。
	if tools := model.GetCommonOptions(&model.Options{}, opts...).Tools; tools != nil {
		m.enabled = false
		for _, tool := range tools {
			if tool.Name == jsonformat.ToolName {
				m.enabled = true
			}
		}
	}
	if !m.enabled {
		return schema.AssistantMessage("disabled", nil), nil
	}
	for _, message := range input {
		if message.Role == schema.Tool {
			*m.results = append(*m.results, message.Content)
			return schema.AssistantMessage("formatted", nil), nil
		}
	}
	args, _ := json.Marshal(map[string]string{"text": "{ \"n\": 1 }", "mode": "compact"})
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "json-call", Type: "function", Function: schema.FunctionCall{Name: jsonformat.ToolName, Arguments: string(args)}}}), nil
}
func (m jsonCapabilityModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestJSONProcessCapabilityEntersAndLeavesActualModelToolSet(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	host, err := pluginhost.New(context.Background(), root, pluginhost.Options{Tools: []pluginhost.ToolSpec{jsonformat.Source()}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	reg := plugin.NewRegistry(plugin.PermissionProcessExec)
	if err := reg.Register(jsonformat.New(host)); err != nil {
		t.Fatal(err)
	}
	var results []string
	runner, err := NewRunner(context.Background(), jsonCapabilityModel{results: &results}, nil, nil, WithCapabilities(reg))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"disabled", "formatted", "disabled"} {
		if i == 1 {
			if err := reg.Enable(jsonformat.PluginID); err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 {
			if err := reg.Disable(jsonformat.PluginID); err != nil {
				t.Fatal(err)
			}
		}
		answer, err := runner.Run(context.Background(), RunRequest{RunID: "json-model", Message: "format this JSON", Sink: &collectingSink{}})
		if err != nil || answer != want {
			t.Fatalf("run %d answer=%q error=%v", i, answer, err)
		}
	}
	if len(results) != 1 || results[0] != "{\"n\":1}" {
		t.Fatalf("real plugin results=%q", results)
	}
}
