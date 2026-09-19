package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
	"luna-agent/internal/config"
	"luna-agent/internal/pluginhost"
)

const ToolName = "luna_text_transform"
const instruction = "You are Luna, a truthful local demo. Use luna_text_transform whenever the user explicitly requests text transformation or explicitly asks to call it. Do not claim tools or actions that were not observed."

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}
type Sink interface{ Emit(Event) }
type sinkKey struct{}
type runIDKey struct{}

func WithRun(ctx context.Context, runID string, sink Sink) context.Context {
	ctx = context.WithValue(ctx, sinkKey{}, sink)
	return context.WithValue(ctx, runIDKey{}, runID)
}
func emit(ctx context.Context, e Event) {
	if s, ok := ctx.Value(sinkKey{}).(Sink); ok && s != nil {
		s.Emit(e)
	}
}
func runID(ctx context.Context) string { v, _ := ctx.Value(runIDKey{}).(string); return v }

type RunStarted struct {
	RunID string `json:"run_id"`
}
type AssistantDelta struct {
	Text string `json:"text"`
}
type ToolStarted struct {
	RunID     string `json:"run_id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}
type ToolFinished struct {
	RunID      string `json:"run_id"`
	Name       string `json:"name"`
	Result     string `json:"result"`
	Generation uint64 `json:"generation"`
	Version    string `json:"version"`
	PluginPID  int    `json:"plugin_pid"`
}
type ToolFailed struct {
	RunID      string `json:"run_id"`
	Name       string `json:"name"`
	Error      string `json:"error"`
	Generation uint64 `json:"generation,omitempty"`
	Version    string `json:"version,omitempty"`
	PluginPID  int    `json:"plugin_pid,omitempty"`
}
type RunFinished struct {
	RunID  string `json:"run_id"`
	Answer string `json:"answer"`
}
type RunFailed struct {
	RunID string `json:"run_id"`
	Error string `json:"error"`
}

type Invoker interface {
	Invoke(context.Context, pluginhost.Input) (pluginhost.Output, error)
}
type TextTransformTool struct{ invoker Invoker }

func NewTextTransformTool(i Invoker) *TextTransformTool { return &TextTransformTool{invoker: i} }
func (t *TextTransformTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo(), nil
}

func strictToolSchema() *jsonschema.Schema {
	// Reflector produces the required object schema and closes additional properties.
	type args struct {
		Text string `json:"text" jsonschema_description:"Text to transform"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"text"}
	return s
}

func (t *TextTransformTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var raw any
	if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
		raw = arguments
	}
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: ToolName, Arguments: raw}})
	var in struct {
		Text string `json:"text"`
	}
	d := json.NewDecoder(strings.NewReader(arguments))
	d.DisallowUnknownFields()
	err := d.Decode(&in)
	if err == nil {
		var extra any
		if extraErr := d.Decode(&extra); extraErr != io.EOF {
			err = extraErr
			if err == nil {
				err = fmt.Errorf("expected exactly one JSON object")
			}
		}
	}
	if err != nil || in.Text == "" {
		if err == nil {
			err = fmt.Errorf("text is required")
		}
		emit(ctx, Event{Type: "tool.failed", Data: ToolFailed{RunID: runID(ctx), Name: ToolName, Error: err.Error()}})
		return "", err
	}
	out, err := t.invoker.Invoke(ctx, pluginhost.Input{Text: in.Text})
	if err != nil {
		emit(ctx, Event{Type: "tool.failed", Data: ToolFailed{RunID: runID(ctx), Name: ToolName, Error: err.Error(), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
		return "", err
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: ToolName, Result: out.Result, Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	b, _ := json.Marshal(out)
	return string(b), nil
}

// toolInfo fixes the exact public schema after construction.
func toolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ToolName, Desc: "Transform text using the active local Luna subprocess plugin.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(strictToolSchema())}
}

var _ tool.InvokableTool = (*TextTransformTool)(nil)

type Runner struct{ runner *adk.Runner }

func NewRunner(ctx context.Context, m model.ToolCallingChatModel, invoker Invoker) (*Runner, error) {
	tt := NewTextTransformTool(invoker)
	a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "luna", Description: "Local Luna core preview", Instruction: instruction, Model: m, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{tt}, ExecuteSequentially: true}}, MaxIterations: 6})
	if err != nil {
		return nil, err
	}
	return &Runner{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: a, EnableStreaming: true})}, nil
}

func NewOpenAIRunner(ctx context.Context, cfg config.Config, invoker Invoker) (*Runner, error) {
	m, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model})
	if err != nil {
		return nil, err
	}
	return NewRunner(ctx, m, invoker)
}

func (r *Runner) Run(parent context.Context, message, id string, sink Sink) (answer string, err error) {
	ctx := WithRun(parent, id, sink)
	emit(ctx, Event{Type: "run.started", Data: RunStarted{RunID: id}})
	defer func() {
		if err != nil {
			emit(ctx, Event{Type: "run.failed", Data: RunFailed{RunID: id, Error: err.Error()}})
		} else {
			emit(ctx, Event{Type: "run.finished", Data: RunFinished{RunID: id, Answer: answer}})
		}
	}()
	iter := r.runner.Query(ctx, message)
	var b strings.Builder
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev == nil {
			continue
		}
		if ev.Err != nil {
			return "", ev.Err
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput
		if mv.Role != schema.Assistant {
			continue
		}
		if mv.IsStreaming {
			sr := mv.MessageStream
			var content []string
			hasToolCalls := false
			for {
				chunk, e := sr.Recv()
				if e == io.EOF {
					break
				}
				if e != nil {
					sr.Close()
					return "", e
				}
				if chunk == nil {
					continue
				}
				if len(chunk.ToolCalls) > 0 {
					hasToolCalls = true
				}
				if chunk.Content != "" {
					content = append(content, chunk.Content)
				}
			}
			sr.Close()
			if !hasToolCalls {
				for _, text := range content {
					b.WriteString(text)
					emit(ctx, Event{Type: "assistant.delta", Data: AssistantDelta{Text: text}})
				}
			}
		} else if mv.Message != nil && len(mv.Message.ToolCalls) == 0 && mv.Message.Content != "" {
			b.WriteString(mv.Message.Content)
			emit(ctx, Event{Type: "assistant.delta", Data: AssistantDelta{Text: mv.Message.Content}})
		}
	}
	answer = b.String()
	if answer == "" {
		return "", fmt.Errorf("agent completed without visible assistant text")
	}
	return answer, nil
}
