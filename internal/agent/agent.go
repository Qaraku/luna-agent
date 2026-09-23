package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const (
	// ToolName and ReadFileToolName are the model-visible names of the two
	// allowlisted tools. The names come from the plugin host's allowlist, so the
	// core cannot register a tool the host cannot route or replace.
	ToolName         = pluginhost.ToolTextTransform
	ReadFileToolName = pluginhost.ToolReadFile
)
const instruction = "You are Luna, a truthful local demo. Use luna_text_transform whenever the user explicitly requests text transformation or explicitly asks to call it. Use luna_read_file when the user asks you to read a file; the path must be relative to the configured read root, which holds the local text files you may read. Do not claim tools or actions that were not observed."

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

// FileReader is the host-side file tool. The wrapper hands it the raw path from
// the model; validating that path against the read root is the host's job, not
// the tool wrapper's and never the plugin's.
type FileReader interface {
	ReadFile(context.Context, pluginhost.ReadRequest) (pluginhost.Output, error)
}

type TextTransformTool struct{ invoker Invoker }

func NewTextTransformTool(i Invoker) *TextTransformTool { return &TextTransformTool{invoker: i} }
func (t *TextTransformTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo(), nil
}

// decodeOne accepts exactly one JSON object and rejects unknown fields and
// trailing JSON values. Both tool wrappers share it so a malformed call is
// refused before it can reach a plugin.
func decodeOne(arguments string, into any) error {
	d := json.NewDecoder(strings.NewReader(arguments))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	var extra any
	if extraErr := d.Decode(&extra); extraErr != io.EOF {
		if extraErr == nil {
			return fmt.Errorf("expected exactly one JSON object")
		}
		return extraErr
	}
	return nil
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
	err := decodeOne(arguments, &in)
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
	// Only the transformed text is model-visible. Plugin generation, version and
	// process identity stay in the event stream for the UI, so they cannot reach
	// a user-facing answer through the model's context.
	return out.Result, nil
}

type ReadFileTool struct{ reader FileReader }

func NewReadFileTool(r FileReader) *ReadFileTool { return &ReadFileTool{reader: r} }
func (t *ReadFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return readFileInfo(), nil
}

func readFileSchema() *jsonschema.Schema {
	type args struct {
		Path string `json:"path" jsonschema_description:"Path of a text file, relative to the configured read root"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"path"}
	return s
}

func (t *ReadFileTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var raw any
	if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
		raw = arguments
	}
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: ReadFileToolName, Arguments: raw}})
	var in struct {
		Path string `json:"path"`
	}
	err := decodeOne(arguments, &in)
	if err != nil || in.Path == "" {
		if err == nil {
			err = fmt.Errorf("path is required")
		}
		emit(ctx, Event{Type: "tool.failed", Data: ToolFailed{RunID: runID(ctx), Name: ReadFileToolName, Error: err.Error()}})
		return "", err
	}
	// The requested path is passed through unchanged: the host resolves and
	// validates it against the read root before any plugin sees it.
	out, err := t.reader.ReadFile(ctx, pluginhost.ReadRequest{Path: in.Path})
	if err != nil {
		emit(ctx, Event{Type: "tool.failed", Data: ToolFailed{RunID: runID(ctx), Name: ReadFileToolName, Error: err.Error(), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
		return "", err
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: ReadFileToolName, Result: out.Result, Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	// As with the transform tool, only the file text is model-visible; the
	// serving generation, version and process identity stay in the event stream.
	return out.Result, nil
}

// toolInfo fixes the exact public schema after construction.
func toolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ToolName, Desc: "Transform text using the active local Luna subprocess plugin.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(strictToolSchema())}
}

// readFileInfo is the exact public schema of the file tool.
func readFileInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ReadFileToolName, Desc: "Read a text file from the configured read root using the active local Luna subprocess plugin.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(readFileSchema())}
}

var (
	_ tool.InvokableTool = (*TextTransformTool)(nil)
	_ tool.InvokableTool = (*ReadFileTool)(nil)
)

type Runner struct{ runner *adk.Runner }

func NewRunner(ctx context.Context, m model.ToolCallingChatModel, invoker Invoker, reader FileReader) (*Runner, error) {
	tools := []tool.BaseTool{NewTextTransformTool(invoker), NewReadFileTool(reader)}
	a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "luna", Description: "Local Luna core preview", Instruction: instruction, Model: m, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}}, MaxIterations: 6})
	if err != nil {
		return nil, err
	}
	return &Runner{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: a, EnableStreaming: true})}, nil
}

func NewOpenAIRunner(ctx context.Context, cfg config.Config, invoker Invoker, reader FileReader) (*Runner, error) {
	m, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model})
	if err != nil {
		return nil, err
	}
	return NewRunner(ctx, m, invoker, reader)
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
