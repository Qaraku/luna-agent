package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const (
	// ToolName, ReadFileToolName, ListDirToolName and SearchFilesToolName are
	// the model-visible names of the plugin-backed tools. The names come from
	// the plugin host's allowlist, so the core cannot register a tool the host
	// cannot route or replace.
	ToolName            = pluginhost.ToolTextTransform
	ReadFileToolName    = pluginhost.ToolReadFile
	ListDirToolName     = pluginhost.ToolListDir
	SearchFilesToolName = pluginhost.ToolSearchFiles
)

// instruction is the whole system instruction the core owns. It states how to
// behave and how to report a refusal; it names no capability's business rules,
// because a capability describes its own tool and contributes its own reference
// block. Text that would have to change when a capability changes does not
// belong here.
const instruction = "You are Luna, a truthful local demo. Use luna_text_transform whenever the user explicitly requests text transformation or explicitly asks to call it; it returns exactly what the active candidate produced, so when the result equals the input, say so plainly instead of guessing that the plugin is broken. Use luna_list_dir to see what a directory contains — when the user asks what is in a directory or in the project, or when you need to discover a path before reading it; the path must be relative to the configured read root, and one call lists one level only: subdirectories are named but not entered, and there is no option to list a whole tree in one call, so walk a repository one directory per call. Use luna_search_files to find where something is written — when you need to locate text, a name or a definition rather than read a file you already know; the query is a literal string and never a pattern, because this tool has no pattern language, so a regular expression is searched as its own characters, and the path says where the search starts, a directory or one file, relative to the configured read root, which nothing outside may be searched. A search looks below the path you give and is bounded, so it can stop at one of its caps; when it does it states the cap it reached and that the remaining paths were not searched, and you must report that limit rather than describe the whole project as covered. Use luna_read_file when the user asks you to read a file; the path must be relative to the configured read root, which holds the local text files you may read. A tool that refuses a call returns a result that begins \"the tool refused this call:\" followed by the reason: report that reason in the user's own language, and do not retry the same call or describe the tool as unavailable. Do not claim tools or actions that were not observed."

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}
type Sink interface{ Emit(Event) }
type sinkKey struct{}
type runIDKey struct{}

// RunRequest is one admitted run. RunID is core-owned; SessionID names the
// durable session the run belongs to and is carried to the client on the
// existing run.started event.
type RunRequest struct {
	RunID     string
	SessionID string
	Message   string
	Sink      Sink
}

// History is the read side of persisted conversation. The history of a run is
// read from disk, never from memory, so a restarted process continues the same
// session.
type History interface {
	Messages(sessionID string) ([]store.MessageRecord, error)
}

// Transcript is the write side of persisted conversation. Each append is one
// complete line, and a failed append fails the run: a run whose transcript
// cannot be written must not report success.
type Transcript interface {
	AppendMessage(sessionID string, record store.MessageRecord) error
	AppendToolCall(sessionID string, record store.ToolCallRecord) error
	AppendRun(sessionID string, record store.RunRecord) error
}

// Option configures the durable side of a Runner.
type Option func(*Runner)

// WithHistory supplies the persisted history a run's model input is assembled
// from.
func WithHistory(h History) Option { return func(r *Runner) { r.history = h } }

// WithTranscript supplies the durable transcript a run appends to.
func WithTranscript(t Transcript) Option { return func(r *Runner) { r.transcript = t } }

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
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id,omitempty"`
}
type AssistantDelta struct {
	Text string `json:"text"`
}
type ToolStarted struct {
	RunID     string `json:"run_id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// ToolFinished is the event a served tool call produces. The identity fields are
// absent for a tool a capability contributes: no process served the call, so
// generation, version and process id are omitted rather than reported as zero.
// Every plugin-backed call still carries all three.
type ToolFinished struct {
	RunID  string `json:"run_id"`
	Name   string `json:"name"`
	Result string `json:"result"`
	// DurationMS is how long the call took, measured by the runtime from the
	// tool.started event that opened it. A client cannot measure this: it does
	// not know when the call began on the server, and a client that guessed
	// would be reporting its own latency as the tool's.
	DurationMS int64  `json:"duration_ms"`
	Generation uint64 `json:"generation,omitempty"`
	Version    string `json:"version,omitempty"`
	PluginPID  int    `json:"plugin_pid,omitempty"`
}
type ToolFailed struct {
	RunID      string `json:"run_id"`
	Name       string `json:"name"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
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

// RunCancelled is the terminal event of a run that was stopped instead of
// broken: a Stop request, or the run deadline. The durable vocabulary does not
// grow with it — store.StatusCancelled already covers both, and the reason
// distinguishes them only for whoever is watching the run.
type RunCancelled struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
}

// AssistantReasoning is a delta of reasoning the provider chose to expose, in the
// same shape as AssistantDelta and on its own event because it is a different
// kind of content: it is streamed, it is shown as part of the run's timeline, and
// it never becomes the answer. A provider that reports no reasoning produces no
// such event — nothing here is ever invented or inferred.
type AssistantReasoning struct {
	Text string `json:"text"`
}

// UsageUpdated reports what one model call cost, when the provider reports it at
// all. Nothing in the runtime depends on it: a provider that stays silent simply
// produces no such event, and tokens the runtime was not told about are never
// estimated.
type UsageUpdated struct {
	RunID           string `json:"run_id"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	TotalTokens     int    `json:"total_tokens"`
	CachedTokens    int    `json:"cached_tokens,omitempty"`
	ReasoningTokens int    `json:"reasoning_tokens,omitempty"`
}

const (
	// CancelReasonUser is the reason of a run the caller stopped: the Stop
	// endpoint, or the request context that owned the stream ending.
	CancelReasonUser = "user"
	// CancelReasonTimeout is the reason of a run the run deadline stopped.
	CancelReasonTimeout = "timeout"
)

// IsTerminalEvent reports whether an event type ends a run. The terminal
// vocabulary lives here, next to the events themselves, so the writer that
// enforces "exactly one terminal event" cannot drift from the events that
// implement it.
func IsTerminalEvent(eventType string) bool {
	switch eventType {
	case "run.finished", "run.failed", "run.cancelled":
		return true
	}
	return false
}

// TerminalEvent is the one event that ends a run. A run that did not succeed
// with an ended context was stopped, not broken, so it is reported as
// run.cancelled with the reason the request side set; anything else is a real
// failure.
func TerminalEvent(ctx context.Context, runID, answer string, err error) Event {
	if err != nil {
		if reason, stopped := cancelReason(ctx, err); stopped {
			return Event{Type: "run.cancelled", Data: RunCancelled{RunID: runID, Reason: reason}}
		}
		return Event{Type: "run.failed", Data: RunFailed{RunID: runID, Error: err.Error()}}
	}
	return Event{Type: "run.finished", Data: RunFinished{RunID: runID, Answer: answer}}
}

// cancelReason names why a run that did not succeed was stopped rather than
// broken. The run's own context is the authority, not the error text: a
// cancellation can surface from the iterator as any wrapped error, while a
// context that ended is unambiguous — and the early terminal synthesis in the
// HTTP layer, which sees only an error and no event, needs the same answer.
func cancelReason(ctx context.Context, err error) (string, bool) {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(cause, context.DeadlineExceeded):
		return CancelReasonTimeout, true
	case errors.Is(err, context.Canceled), cause != nil:
		return CancelReasonUser, true
	}
	return "", false
}

type Invoker interface {
	Invoke(context.Context, pluginhost.Input) (pluginhost.Output, error)
}

// refusalPrefix opens the tool result the model sees when a tool refuses a call.
const refusalPrefix = "the tool refused this call: "

// isInfrastructure reports whether err means the tool never ran because its
// owned plugin could not serve the call. Everything else is a refusal: the tool
// declined this particular call for a reason the model can act on or explain.
//
// The distinction is a classification, not a message match: the plugin host
// tags its own failures with sentinels, and a plugin's own refusal crosses
// net/rpc as plain text and is therefore never one of them.
func isInfrastructure(err error) bool {
	return errors.Is(err, pluginhost.ErrUnknownTool) ||
		errors.Is(err, pluginhost.ErrNoActivePlugin) ||
		errors.Is(err, pluginhost.ErrRPCTimeout) ||
		errors.Is(err, pluginhost.ErrRPCCanceled) ||
		errors.Is(err, pluginhost.ErrPluginGone)
}

// refuse reports a failed tool call. Either way the UI sees tool.failed with the
// tool's own message; the difference is what happens to the run. A refusal is
// the tool's answer about the call itself, so the reason becomes the call's
// result and the run continues — the model is the only participant that can
// explain it to the user or try a different call. An infrastructure failure ends
// the run, because a model cannot be told anything useful about a plugin that is
// not there.
func refuse(ctx context.Context, name string, out pluginhost.Output, err error, startedAt time.Time) (string, error) {
	emit(ctx, Event{Type: "tool.failed", Data: ToolFailed{RunID: runID(ctx), Name: name, Error: err.Error(), DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	if isInfrastructure(err) {
		return "", err
	}
	return refusalPrefix + err.Error(), nil
}

// callDurationMS is how long one tool call has taken so far, measured from the
// tool.started event the wrapper emitted when it accepted the call. The runtime
// times the call because it is the only side that knows when the call began.
func callDurationMS(startedAt time.Time) int64 { return time.Since(startedAt).Milliseconds() }

// stopCall closes a tool call the run's own cancellation interrupted. The call
// still ends in the event stream, so the browser can finish it and show how long
// it ran, but the error ends the run instead of reaching the model as a refusal:
// the tool did not decline anything, the run was stopped, and telling the model
// otherwise would be a false claim about what happened.
func stopCall(ctx context.Context, name string, reason error, out pluginhost.Output, startedAt time.Time) (string, error) {
	emit(ctx, Event{Type: "tool.failed", Data: ToolFailed{RunID: runID(ctx), Name: name, Error: reason.Error(), DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	return "", reason
}

// emitUsage forwards a usage block one model call reported. Providers differ:
// the OpenAI-compatible path asks for it with StreamOptions.IncludeUsage, and a
// provider that reports nothing produces no event rather than zeros.
func emitUsage(ctx context.Context, runID string, usage *schema.TokenUsage) {
	if usage == nil {
		return
	}
	emit(ctx, Event{Type: "usage.updated", Data: UsageUpdated{
		RunID:           runID,
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		TotalTokens:     usage.TotalTokens,
		CachedTokens:    usage.PromptTokenDetails.CachedTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
	}})
}

// emitMessageUsage is emitUsage for a call that arrived whole.
func emitMessageUsage(ctx context.Context, runID string, message *schema.Message) {
	if message == nil || message.ResponseMeta == nil {
		return
	}
	emitUsage(ctx, runID, message.ResponseMeta.Usage)
}

// FileTools is the host-side file capability the core's plugin-backed file tools
// depend on: reading one file, and listing one directory. Both wrappers hand it
// the raw path from the model; validating that path against the read root is the
// host's job, not the tool wrapper's and never the plugin's. Listing carries no
// recursion knob, so the interface cannot ask for more than one level.
type FileTools interface {
	ReadFile(context.Context, pluginhost.ReadRequest) (pluginhost.Output, error)
	ListDir(context.Context, pluginhost.ListRequest) (pluginhost.Output, error)
}

// FileSearcher is the host-side half of luna_search_files: one bounded literal
// search of a path the host has already validated, of a file or of a directory.
// It is a separate interface rather than a third method on FileTools because the
// two are independent halves of one host — the core registers the search wrapper
// only for a file capability that serves a search, so it never offers the model a
// tool whose call would fail for want of a host half.
type FileSearcher interface {
	SearchFiles(context.Context, pluginhost.SearchRequest) (pluginhost.Output, error)
}

// The host the core is assembled with serves every plugin-backed wrapper the
// core owns. Pinning it here makes a host that stopped serving one a compile
// error rather than a tool that disappears from the model's set.
var _ FileSearcher = (*pluginhost.Host)(nil)

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
	startedAt := time.Now()
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: ToolName, Arguments: raw}})
	var in struct {
		Text string `json:"text"`
	}
	err := decodeOne(arguments, &in)
	if err != nil || in.Text == "" {
		if err == nil {
			err = fmt.Errorf("text is required")
		}
		return refuse(ctx, ToolName, pluginhost.Output{}, err, startedAt)
	}
	out, err := t.invoker.Invoke(ctx, pluginhost.Input{Text: in.Text})
	if err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, ToolName, stopped, out, startedAt)
		}
		return refuse(ctx, ToolName, out, err, startedAt)
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: ToolName, Result: out.Result, DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	// Only the transformed text is model-visible. Plugin generation, version and
	// process identity stay in the event stream for the UI, so they cannot reach
	// a user-facing answer through the model's context.
	return out.Result, nil
}

type ReadFileTool struct{ reader FileTools }

func NewReadFileTool(r FileTools) *ReadFileTool { return &ReadFileTool{reader: r} }
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
	startedAt := time.Now()
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: ReadFileToolName, Arguments: raw}})
	var in struct {
		Path string `json:"path"`
	}
	err := decodeOne(arguments, &in)
	if err != nil || in.Path == "" {
		if err == nil {
			err = fmt.Errorf("path is required")
		}
		return refuse(ctx, ReadFileToolName, pluginhost.Output{}, err, startedAt)
	}
	// The requested path is passed through unchanged: the host resolves and
	// validates it against the read root before any plugin sees it.
	out, err := t.reader.ReadFile(ctx, pluginhost.ReadRequest{Path: in.Path})
	if err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, ReadFileToolName, stopped, out, startedAt)
		}
		return refuse(ctx, ReadFileToolName, out, err, startedAt)
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: ReadFileToolName, Result: out.Result, DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	// As with the transform tool, only the file text is model-visible; the
	// serving generation, version and process identity stay in the event stream.
	return out.Result, nil
}

// toolInfo fixes the exact public schema after construction.
func toolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ToolName, Desc: "Transform text using the active local Luna subprocess plugin.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(strictToolSchema())}
}

// ListDirTool is the model-visible directory-listing tool. It is the listing
// twin of ReadFileTool: the wrapper refuses a malformed call, the host refuses a
// path the read root does not hold, and the plugin only ever renders a directory
// the host resolved.
type ListDirTool struct{ lister FileTools }

func NewListDirTool(l FileTools) *ListDirTool { return &ListDirTool{lister: l} }
func (t *ListDirTool) Info(context.Context) (*schema.ToolInfo, error) {
	return listDirInfo(), nil
}

// listDirSchema is deliberately one parameter. A recursion depth, a glob or a
// filter would turn one listing into an unbounded walk of the read root, so the
// schema offers no way to ask for one.
func listDirSchema() *jsonschema.Schema {
	type args struct {
		Path string `json:"path" jsonschema_description:"Directory path, relative to the configured read root; use \".\" for the read root itself"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"path"}
	return s
}

func (t *ListDirTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var raw any
	if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
		raw = arguments
	}
	startedAt := time.Now()
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: ListDirToolName, Arguments: raw}})
	var in struct {
		Path string `json:"path"`
	}
	err := decodeOne(arguments, &in)
	if err != nil || in.Path == "" {
		if err == nil {
			err = fmt.Errorf("path is required")
		}
		return refuse(ctx, ListDirToolName, pluginhost.Output{}, err, startedAt)
	}
	// As with a read, the requested path is passed through unchanged: the host
	// resolves and validates it against the read root before any plugin sees it.
	out, err := t.lister.ListDir(ctx, pluginhost.ListRequest{Path: in.Path})
	if err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, ListDirToolName, stopped, out, startedAt)
		}
		return refuse(ctx, ListDirToolName, out, err, startedAt)
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: ListDirToolName, Result: out.Result, DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	// Only the rendered listing is model-visible; the serving generation,
	// version and process identity stay in the event stream for the UI.
	return out.Result, nil
}

// listDirInfo is the exact public schema of the listing tool. The description
// states the two things a model gets wrong about a directory tool: the path is
// relative to the read root, and one call is one level — it names subdirectories
// without entering them and offers no recursion, so a whole tree is walked one
// directory at a time. It never carries the absolute host path.
func listDirInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ListDirToolName, Desc: "List the entries of one directory in the configured read root using the active local Luna subprocess plugin. The path must be relative to the read root, the directory this server was started in, and \".\" means the read root itself; an absolute path or one outside the read root is refused. Exactly one level is listed: a subdirectory appears as an entry and is not entered, and there is no recursion option. Each line gives the kind (dir, file, link, other), a size for regular files and the name; directories come first, then files and links, each sorted by name. A directory with more entries than one listing renders says how many were left out instead of dropping them silently.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(listDirSchema())}
}

// readFileInfo is the exact public schema of the file tool. The description
// names the read root so the model does not have to guess what a relative path
// is relative to; it never carries the absolute host path.
func readFileInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ReadFileToolName, Desc: "Read a text file from the configured read root using the active local Luna subprocess plugin. The path must be relative to the read root, the directory this server was started in; an absolute path or one outside the read root is refused.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(readFileSchema())}
}

// SearchFilesTool is the model-visible literal-search tool. It is the third
// member of the file family: the wrapper refuses a malformed call, the host
// refuses a path the read root does not hold and a literal it cannot search, and
// the plugin searches only what the host resolved, in lines of the text it can
// read.
type SearchFilesTool struct{ searcher FileSearcher }

func NewSearchFilesTool(s FileSearcher) *SearchFilesTool { return &SearchFilesTool{searcher: s} }
func (t *SearchFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return searchFilesInfo(), nil
}

// searchFilesSchema is deliberately two parameters: where to start, and the
// literal to find. A depth, a glob, a filter or a pattern flag would let one call
// widen itself past a bounded literal search, so the schema offers no way to ask
// for one.
func searchFilesSchema() *jsonschema.Schema {
	type args struct {
		Path  string `json:"path" jsonschema_description:"Directory to search below, or one file to search, relative to the configured read root; use \".\" for the read root itself"`
		Query string `json:"query" jsonschema_description:"Literal text to find inside single lines; nothing in it is interpreted, so a regular expression is only those characters"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"path", "query"}
	return s
}

func (t *SearchFilesTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var raw any
	if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
		raw = arguments
	}
	startedAt := time.Now()
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: SearchFilesToolName, Arguments: raw}})
	var in struct {
		Path  string `json:"path"`
		Query string `json:"query"`
	}
	err := decodeOne(arguments, &in)
	if err == nil && in.Path == "" {
		err = fmt.Errorf("path is required")
	}
	if err == nil && in.Query == "" {
		err = fmt.Errorf("query is required: a search needs a literal to look for")
	}
	if err != nil {
		return refuse(ctx, SearchFilesToolName, pluginhost.Output{}, err, startedAt)
	}
	// As with a read or a listing, the requested path is passed through
	// unchanged: the host resolves and validates it against the read root before
	// any plugin sees it, and the literal is validated there too.
	out, err := t.searcher.SearchFiles(ctx, pluginhost.SearchRequest{Path: in.Path, Query: in.Query})
	if err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, SearchFilesToolName, stopped, out, startedAt)
		}
		return refuse(ctx, SearchFilesToolName, out, err, startedAt)
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: SearchFilesToolName, Result: out.Result, DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	// Only the rendered matches are model-visible; the serving generation,
	// version and process identity stay in the event stream for the UI.
	return out.Result, nil
}

// searchFilesInfo is the exact public schema of the search tool. The description
// states the four things a model gets wrong about a search: the query is a
// literal and not a pattern, the path says where the search starts and is
// relative to the read root, a search looks below that path but is bounded and
// says so when it stops, and a hit names its file relative to the path that was
// searched. It never carries the absolute host path.
func searchFilesInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: SearchFilesToolName, Desc: "Search the configured read root for a literal string, using the active local Luna subprocess plugin. Both arguments are required. path says where the search starts — a directory to search below, or one file — and must be relative to the read root, the directory this server was started in, where \".\" means the read root itself; an absolute path, a path outside the read root, or a path that is neither a file nor a directory is refused. query is the literal text to find, matched inside single lines exactly as written: there is no pattern language here, so a regular expression is searched as its own characters and a dot is a dot. A directory is walked below itself in path order, and a symbolic link is never followed, so a search cannot leave the read root. Each result line is <path>:<line>: <line text>, with <path> relative to the path that was searched. A search is bounded — how many matches are rendered, how long one line may be, how many files are read and how large a file may be all have caps — and when it stops at one it says which cap it reached and that the remaining paths were not searched, so a result that reports a cap has not seen the whole tree; content that is not text is counted rather than searched.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(searchFilesSchema())}
}

var (
	_ tool.InvokableTool = (*TextTransformTool)(nil)
	_ tool.InvokableTool = (*ReadFileTool)(nil)
	_ tool.InvokableTool = (*ListDirTool)(nil)
	_ tool.InvokableTool = (*SearchFilesTool)(nil)
)

type Runner struct {
	runner     *adk.Runner
	history    History
	transcript Transcript
	// capabilities is the registry of enabled contributions. The runner
	// assembles their tools and context blocks; it knows nothing about what any
	// of them means.
	capabilities *plugin.Registry
}

// NewRunner builds the agent and its tool set. Every model-visible tool is
// registered here, by the core: the plugin-backed wrappers whose host-side half
// the runner was handed, and one wrapper per tool contributed by an enabled
// capability.
func NewRunner(ctx context.Context, m model.ToolCallingChatModel, invoker Invoker, files FileTools, opts ...Option) (*Runner, error) {
	r := &Runner{}
	for _, opt := range opts {
		opt(r)
	}
	tools := []tool.BaseTool{NewTextTransformTool(invoker), NewReadFileTool(files), NewListDirTool(files)}
	// The search wrapper is registered exactly for a file capability that serves
	// a search: the core offers the model the plugin-backed tools it can route,
	// and never a tool whose call would fail for want of a host half.
	if searcher, ok := files.(FileSearcher); ok {
		tools = append(tools, NewSearchFilesTool(searcher))
	}
	for _, contributed := range r.capabilityTools() {
		tools = append(tools, contributed)
	}
	a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "luna", Description: "Local Luna core preview", Instruction: instruction, GenModelInput: r.modelInputWithCapabilities(), Model: m, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}}, MaxIterations: 6})
	if err != nil {
		return nil, err
	}
	r.runner = adk.NewRunner(ctx, adk.RunnerConfig{Agent: a, EnableStreaming: true})
	return r, nil
}

func NewOpenAIRunner(ctx context.Context, cfg config.Config, invoker Invoker, files FileTools, opts ...Option) (*Runner, error) {
	modelConfig := &openai.ChatModelConfig{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model}
	if cfg.ReasoningEffort != "" {
		// Only set when a level was chosen: the field is then left out of the
		// request entirely, which is what keeps this knob from reaching providers
		// that do not define it. How hard the model thinks is a parameter of the
		// run; showing the reasoning it produced is a separate concern and does
		// not depend on this being set.
		modelConfig.ReasoningEffort = openai.ReasoningEffortLevel(cfg.ReasoningEffort)
	}
	m, err := openai.NewChatModel(ctx, modelConfig)
	if err != nil {
		return nil, err
	}
	return NewRunner(ctx, m, invoker, files, opts...)
}

// The history cap is stated here, in the S2a spec's terms: a run's model input
// is the system prompt, then the session's prior messages in order, then this
// turn's user message. The prior messages are capped, and the policy is
// deterministic — keep the most recent ones and drop the oldest. Summarization
// and retrieval are deliberately absent; durable memory is a separate store,
// injected into the system prompt and capped by MaxInjectFacts/MaxInjectBytes.
const (
	// MaxHistoryMessages is the largest number of prior messages kept.
	MaxHistoryMessages = 40
	// MaxHistoryBytes is the largest total size of the kept prior messages, in
	// UTF-8 bytes of message text. It sits above the 16,384-byte HTTP message
	// cap, so this turn's own message always fits.
	MaxHistoryBytes = 64 * 1024
)

// selectHistory returns the messages that fit the cap, oldest dropped first:
// the kept set is a contiguous suffix of the persisted history, accumulated
// from the newest message backwards. The scan stops at the first message that
// would exceed either MaxHistoryMessages or MaxHistoryBytes, so messages are
// never reordered, sampled, or skipped over.
func selectHistory(messages []store.MessageRecord) []store.MessageRecord {
	kept, size := 0, 0
	for i := len(messages) - 1; i >= 0; i-- {
		messageSize := len(messages[i].Text)
		if kept == MaxHistoryMessages || size+messageSize > MaxHistoryBytes {
			break
		}
		kept++
		size += messageSize
	}
	if kept == 0 {
		return nil
	}
	return append([]store.MessageRecord{}, messages[len(messages)-kept:]...)
}

// priorMessages reads the session's history from disk, drops this run's own
// records — the user message is persisted before the model runs — and applies
// the documented cap.
func (r *Runner) priorMessages(req RunRequest) ([]store.MessageRecord, error) {
	if r.history == nil || req.SessionID == "" {
		return nil, nil
	}
	records, err := r.history.Messages(req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load session history: %w", err)
	}
	prior := make([]store.MessageRecord, 0, len(records))
	for _, record := range records {
		if record.RunID == req.RunID {
			continue
		}
		if record.Role != store.RoleUser && record.Role != store.RoleAssistant {
			// An unknown role is dropped rather than guessed at.
			continue
		}
		prior = append(prior, record)
	}
	return selectHistory(prior), nil
}

// modelInput assembles what the model sees for this turn. The system prompt is
// not repeated here: the Eino agent prepends its instruction to exactly this
// input. Only message text enters the input, so no plugin identity and no tool
// result can leak into the model's context through the persisted history.
func (r *Runner) modelInput(req RunRequest) ([]*schema.Message, error) {
	prior, err := r.priorMessages(req)
	if err != nil {
		return nil, err
	}
	input := make([]*schema.Message, 0, len(prior)+1)
	for _, message := range prior {
		if message.Role == store.RoleAssistant {
			input = append(input, schema.AssistantMessage(message.Text, nil))
			continue
		}
		input = append(input, schema.UserMessage(message.Text))
	}
	return append(input, schema.UserMessage(req.Message)), nil
}

// runStatus maps a run outcome onto the frozen status vocabulary.
// store.StatusInterrupted is reserved for a run that left no run line at all
// (a crash); this slice never writes it, because a record that was not
// persisted cannot be reported.
func runStatus(err error) string {
	switch {
	case err == nil:
		return store.StatusOK
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return store.StatusCancelled
	default:
		return store.StatusError
	}
}

func (r *Runner) appendMessage(req RunRequest, role, text string, at time.Time) error {
	if r.transcript == nil || req.SessionID == "" {
		return nil
	}
	if err := r.transcript.AppendMessage(req.SessionID, store.MessageRecord{RunID: req.RunID, Role: role, Text: text, At: at}); err != nil {
		return fmt.Errorf("persist %s message: %w", role, err)
	}
	return nil
}

func (r *Runner) appendRun(req RunRequest, startedAt time.Time, status string) error {
	if r.transcript == nil || req.SessionID == "" {
		return nil
	}
	if err := r.transcript.AppendRun(req.SessionID, store.RunRecord{RunID: req.RunID, StartedAt: startedAt, EndedAt: time.Now(), Status: status}); err != nil {
		return fmt.Errorf("persist run record: %w", err)
	}
	return nil
}

func (r *Runner) Run(parent context.Context, req RunRequest) (answer string, err error) {
	startedAt := time.Now()
	recorder := newRecorder(req.Sink, r.transcript, req.SessionID, req.RunID)
	// The run identity travels in the context so a capability's tools can
	// attribute what they store without being handed the session themselves.
	ctx := plugin.WithRun(WithRun(parent, req.RunID, recorder), plugin.RunInfo{RunID: req.RunID, SessionID: req.SessionID})
	emit(ctx, Event{Type: "run.started", Data: RunStarted{RunID: req.RunID, SessionID: req.SessionID}})
	defer func() {
		status := runStatus(err)
		if appendErr := r.appendRun(req, startedAt, status); appendErr != nil && err == nil {
			// The run itself succeeded but its transcript entry did not: the
			// run is reported as failed rather than as a success that cannot
			// survive a restart.
			err = appendErr
		}
		emit(ctx, TerminalEvent(ctx, req.RunID, answer, err))
	}()
	// The user message is persisted before the model runs, so a crash mid-run
	// still records what was asked; assembly drops this run's own records
	// instead of depending on that ordering.
	if err := r.appendMessage(req, store.RoleUser, req.Message, startedAt); err != nil {
		return "", err
	}
	input, err := r.modelInput(req)
	if err != nil {
		return "", err
	}
	iter := r.runner.Run(ctx, input)
	var b strings.Builder
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if err := recorder.Err(); err != nil {
			return "", err
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
			// There is deliberately no model.started event here. The stream
			// handle is not a reliable ordering point: Eino hands the model
			// stream to two consumers through Copy(2) — this loop and the
			// graph's own tool branch — so a turn's tool can start before this
			// loop is handed the handle, and "started" would be reported after
			// its own tool call. A marker whose order is decided by the
			// scheduler does not belong in the event stream; the browser
			// derives "waiting for the model" from run.started, tool.finished
			// and the first delta, all of which are in order.
			if cancelErr := ctx.Err(); cancelErr != nil {
				return "", cancelErr
			}
			sr := mv.MessageStream
			var turn strings.Builder
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
					// Delta out as it arrives: this is what makes the answer
					// visible while it is being written. Whether this turn is
					// the answer is only knowable when it ends, so a turn that
					// turns out to carry tool calls is demoted by the browser to
					// a run note; the accumulation below is unaffected, and the
					// answer stays the one in run.finished.
					emit(ctx, Event{Type: "assistant.delta", Data: AssistantDelta{Text: chunk.Content}})
					turn.WriteString(chunk.Content)
				}
				// Reasoning the provider exposed is streamed on its own event and
				// kept out of the answer: it is the run's reasoning, not its
				// reply. It arrives before the content of the same turn, which is
				// why the timeline can show thinking and then the answer.
				if chunk.ReasoningContent != "" {
					emit(ctx, Event{Type: "assistant.reasoning", Data: AssistantReasoning{Text: chunk.ReasoningContent}})
				}
				if usage := chunk.ResponseMeta; usage != nil {
					emitUsage(ctx, req.RunID, usage.Usage)
				}
			}
			sr.Close()
			if !hasToolCalls {
				b.WriteString(turn.String())
			}
		} else if mv.Message != nil {
			emitMessageUsage(ctx, req.RunID, mv.Message)
			if mv.Message.ReasoningContent != "" {
				emit(ctx, Event{Type: "assistant.reasoning", Data: AssistantReasoning{Text: mv.Message.ReasoningContent}})
			}
			if len(mv.Message.ToolCalls) == 0 && mv.Message.Content != "" {
				b.WriteString(mv.Message.Content)
				emit(ctx, Event{Type: "assistant.delta", Data: AssistantDelta{Text: mv.Message.Content}})
			}
		}
	}
	// A run whose context ended was stopped, and the terminal event is the
	// run's own word for how it ended: partial text must never be handed
	// forward as a finished answer, even if the iterator ended without an
	// error of its own.
	if cancelErr := ctx.Err(); cancelErr != nil {
		return "", cancelErr
	}
	if err := recorder.Err(); err != nil {
		return "", err
	}
	answer = b.String()
	if answer == "" {
		return "", fmt.Errorf("agent completed without visible assistant text")
	}
	if err := r.appendMessage(req, store.RoleAssistant, answer, time.Now()); err != nil {
		return "", err
	}
	return answer, nil
}

// recorder persists a run's tool calls while forwarding every event unchanged to
// the real sink, so neither the event stream nor the model-visible answer is
// affected by persistence. Only the frozen tool_call fields are written:
// plugin generation, version and process id stay in the event stream, and the
// store has no field for them, so identity cannot reach the model through the
// replay path either. A failed append is kept and returned to the run, which
// fails rather than reporting a run that left no transcript.
type recorder struct {
	sink       Sink
	transcript Transcript
	sessionID  string
	runID      string

	mu      sync.Mutex
	pending *store.ToolCallRecord
	err     error
}

func newRecorder(sink Sink, transcript Transcript, sessionID, runID string) *recorder {
	return &recorder{sink: sink, transcript: transcript, sessionID: sessionID, runID: runID}
}

func (r *recorder) Emit(e Event) {
	r.persist(e)
	r.sink.Emit(e)
}

// Err is the first persistence failure of the run.
func (r *recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *recorder) persist(e Event) {
	if r.transcript == nil || r.sessionID == "" {
		return
	}
	switch e.Type {
	case "tool.started":
		started, ok := e.Data.(ToolStarted)
		if !ok {
			return
		}
		r.mu.Lock()
		r.pending = &store.ToolCallRecord{Type: store.TypeToolCall, RunID: r.runID, Name: started.Name, Arguments: argumentsText(started.Arguments), At: time.Now()}
		r.mu.Unlock()
	case "tool.finished":
		finished, ok := e.Data.(ToolFinished)
		if !ok {
			return
		}
		r.complete(finished.Name, finished.Result, "")
	case "tool.failed":
		failed, ok := e.Data.(ToolFailed)
		if !ok {
			return
		}
		r.complete(failed.Name, "", failed.Error)
	}
}

// complete writes the tool_call line for the call a tool.started opened. Tool
// calls execute sequentially, so at most one call is pending.
func (r *recorder) complete(name, result, errText string) {
	record := store.ToolCallRecord{Type: store.TypeToolCall, RunID: r.runID, Name: name, Result: result, Error: errText, At: time.Now()}
	r.mu.Lock()
	if r.pending != nil {
		record.Name = r.pending.Name
		record.Arguments = r.pending.Arguments
		r.pending = nil
	}
	r.mu.Unlock()
	if err := r.transcript.AppendToolCall(r.sessionID, record); err != nil {
		r.fail(fmt.Errorf("persist tool call %s: %w", record.Name, err))
	}
}

func (r *recorder) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

// argumentsText keeps the model's own argument text: a JSON object as JSON, and
// the raw string when the model produced something that was not JSON.
func argumentsText(arguments any) string {
	switch typed := arguments.(type) {
	case nil:
		return ""
	case string:
		return typed
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return ""
	}
	return string(encoded)
}
