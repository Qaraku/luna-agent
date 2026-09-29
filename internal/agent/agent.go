package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/runconfig"
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
	// ToolName, ReadFileToolName, ListDirToolName, SearchFilesToolName and
	// FindFilesToolName are
	// the model-visible names of the plugin-backed tools. The names come from
	// the plugin host's allowlist, so the core cannot register a tool the host
	// cannot route or replace.
	ToolName            = pluginhost.ToolTextTransform
	ReadFileToolName    = pluginhost.ToolReadFile
	ListDirToolName     = pluginhost.ToolListDir
	SearchFilesToolName = pluginhost.ToolSearchFiles
	FindFilesToolName   = pluginhost.ToolFindFiles
)

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
//
// Model names the entry of the configured model list this run is sent to. The
// empty string means the configured default, which is the list's first entry.
// A name that is not in the list fails the run: falling back to the default
// would show the user a switch that never happened.
type RunRequest struct {
	RunID       string
	SessionID   string
	WorkspaceID string
	Message     string
	Model       string
	// ReasoningEffort 为 nil 时继承全局，指向空字符串时明确不发送思考字段。
	Setup         *runconfig.Selection
	configuration *runconfig.Snapshot
	// Configured 是宿主的只读观察入口，不能改变本轮配置。
	Configured         func(*runconfig.Snapshot)
	ReasoningEffort    *string
	ExecutionMode      plugin.ExecutionMode
	Permissions        *plugin.AccessPolicy
	AutomaticWriteDirs []string
	WriteScopeError    string
	Approve            plugin.ApprovalFunc
	Sink               Sink
	// Roots are the directories this run works in — the directories of the
	// workspace its session is bound to — in the order the file tools try them.
	// They are resolved by the caller, from the session's own configuration,
	// and they are not model-visible: no tool argument can add one, and a tool
	// call can only be checked against them. An empty list means the run works
	// in no particular directory, and the file tools fall back to the host's
	// single configured root, which is how every unbound session behaves.
	Roots []string
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

// WithRoots puts the directories a run works in on its context, so the file
// tools a capability contributed are checked against the run's own working
// directories without being handed the session. An empty list is carried as an
// empty list rather than replaced here: the host applies its default root, and
// doing it twice would hide which layer decided the range.
//
// The context slot belongs to the plugin substrate (plugin.WithRoots): the
// directories a run works in are a single piece of context, and the file tools,
// the shell tool and anything else that needs them read it through the same
// accessor.
func WithRoots(ctx context.Context, roots []string) context.Context {
	return plugin.WithRoots(ctx, roots)
}
func emit(ctx context.Context, e Event) {
	if s, ok := ctx.Value(sinkKey{}).(Sink); ok && s != nil {
		s.Emit(e)
	}
}
func runID(ctx context.Context) string { v, _ := ctx.Value(runIDKey{}).(string); return v }

// roots is the set of directories the current run works in, and nil when the
// run named none. It reads the plugin substrate's own slot, so a capability
// tool and the core see the same working directories.
func roots(ctx context.Context) []string { return plugin.Roots(ctx) }

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

// UsageUpdated 是本轮累计快照；客户端按 run_id 替换，不能把事件再次相加。
type UsageUpdated struct {
	RunID string `json:"run_id"`
	Scope string `json:"scope"`
	store.Usage
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

// FileTools is the host-side file capability the core's plugin-backed file tools
// depend on: reading one file, and listing one directory. Both wrappers hand it
// the raw path from the model; validating that path against the read root is the
// host's job, not the tool wrapper's and never the plugin's. A listing carries a
// depth the wrapper has already bounded, and the host refuses one outside the
// range on its own terms as well, so the interface cannot ask for more levels
// than the listing has.
type FileTools interface {
	ReadFile(context.Context, pluginhost.ReadRequest) (pluginhost.Output, error)
	ListDir(context.Context, pluginhost.ListRequest) (pluginhost.Output, error)
}

// FileSearcher is the host-side half of luna_search_files: one bounded search
// of a path the host has already validated, of a file or of a directory, read
// either as literal text or as the pattern the call asks for.
// It is a separate interface rather than a third method on FileTools because the
// two are independent halves of one host — the core registers the search wrapper
// only for a file capability that serves a search, so it never offers the model a
// tool whose call would fail for want of a host half.
type FileSearcher interface {
	SearchFiles(context.Context, pluginhost.SearchRequest) (pluginhost.Output, error)
}

// FileFinder is the host-side half of luna_find_files: one bounded walk that
// matches a glob against the entry names below a path the host has already
// validated, of a file or of a directory. It is a second separate interface for
// the same reason FileSearcher is one: the core registers the find wrapper only
// for a file capability that serves a find, so it never offers the model a tool
// whose call would fail for want of a host half.
type FileFinder interface {
	FindFiles(context.Context, pluginhost.FindRequest) (pluginhost.Output, error)
}

// The host the core is assembled with serves every plugin-backed wrapper the
// core owns. Pinning it here makes a host that stopped serving one a compile
// error rather than a tool that disappears from the model's set.
var _ FileSearcher = (*pluginhost.Host)(nil)
var _ FileFinder = (*pluginhost.Host)(nil)

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
		Path string `json:"path" jsonschema_description:"Path of a text file, relative to one of the directories this session works in"`
		// The line range is optional and only needed for a file too large to
		// read whole: a read over the byte cap is refused, and this is how a
		// part of it is reached instead.
		StartLine int `json:"start_line,omitempty" jsonschema_description:"First line to return, counting from 1. Omit to start at the beginning"`
		MaxLines  int `json:"max_lines,omitempty" jsonschema_description:"Largest number of lines to return. Omit to read to the end, as far as the byte cap allows"`
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
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		MaxLines  int    `json:"max_lines"`
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
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: ReadFileToolName, Summary: "read project files", Target: in.Path, ReadRoots: roots(ctx), Permissions: []plugin.AccessKind{plugin.AccessRead}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, ReadFileToolName, stopped, pluginhost.Output{}, startedAt)
		}
		return refuse(ctx, ReadFileToolName, pluginhost.Output{}, err, startedAt)
	}
	out, err := t.reader.ReadFile(ctx, pluginhost.ReadRequest{Path: in.Path, Roots: roots(ctx), StartLine: in.StartLine, MaxLines: in.MaxLines})
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

// listDirSchema fixes the two arguments of a listing: the path to list, and an
// optional depth. The depth is bounded at both ends by the listing itself rather
// than by the schema alone — the wrapper refuses a value outside the range before
// the host sees it — and there is deliberately no glob, filter or sort knob: what
// a listing answers is what a directory holds, in the one order it has always
// rendered, and a depth only says how far down it looks.
func listDirSchema() *jsonschema.Schema {
	type args struct {
		Path string `json:"path" jsonschema_description:"Directory path, relative to one of the directories this session works in; use \".\" for the first of them"`
		// The depth is optional, and saying nothing about it means one level:
		// an integer with a range is what the model reads as "you may ask for
		// more, up to this much".
		Depth int `json:"depth,omitempty" jsonschema_description:"How many levels below the path to list, 1 to 5. Omit for 1, the listing of that one directory; a value outside 1 to 5 is refused before anything is looked at"`
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
		// Depth is a pointer so an explicit zero is distinguishable from an
		// omitted depth: omitting it means the default of one level, and asking
		// for zero levels is a call the listing refuses and explains.
		Depth *int `json:"depth"`
	}
	err := decodeOne(arguments, &in)
	if err == nil && in.Path == "" {
		err = fmt.Errorf("path is required")
	}
	// The depth is refused here, before the host is asked to resolve anything:
	// the range is the listing's own, stated once in internal/fileread, and a
	// call that named a depth the listing does not have must not reach a
	// directory at all.
	if err == nil && in.Depth != nil {
		err = fileread.ValidateNamedDepth(*in.Depth)
	}
	if err != nil {
		return refuse(ctx, ListDirToolName, pluginhost.Output{}, err, startedAt)
	}
	// An omitted depth is the default, and it is sent explicitly: every request
	// the host sees names the depth it is to cover.
	depth := fileread.DefaultListDepth
	if in.Depth != nil {
		depth = *in.Depth
	}
	// As with a read, the requested path is passed through unchanged: the host
	// resolves and validates it against the read root before any plugin sees it.
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: ListDirToolName, Summary: "read project files", Target: in.Path, ReadRoots: roots(ctx), Permissions: []plugin.AccessKind{plugin.AccessRead}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, ListDirToolName, stopped, pluginhost.Output{}, startedAt)
		}
		return refuse(ctx, ListDirToolName, pluginhost.Output{}, err, startedAt)
	}
	out, err := t.lister.ListDir(ctx, pluginhost.ListRequest{Path: in.Path, Depth: depth, Roots: roots(ctx)})
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
// states the four things a model gets wrong about a directory tool: the path is
// relative to the read root, an optional depth covers more than one level and is
// bounded at both ends, the walk is depth-first with paths relative to the
// directory that was named, and the result is bounded and names its own caps. It
// never carries the absolute host path. The caps are interpolated from
// internal/fileread rather than written out here, so the description cannot
// describe a cap the listing no longer has.
func listDirInfo() *schema.ToolInfo {
	desc := fmt.Sprintf(`List the entries of one directory in one of the directories this session works in using the active local Luna subprocess plugin. The path must be relative to one of the directories this session works in (a single configured root when it works in none), and "." means the first of them; an absolute path or one outside them is refused. depth is optional: %d to %d levels, and %d — the entries of that one directory — when it is omitted; any other value, including 0, is refused before anything is looked at. One level is listed by default: a subdirectory appears as an entry and is not entered. A greater depth lists that many levels in one call, depth-first, and below the first level the name on each line is that entry's path relative to the directory you named, with a subdirectory's own entries on the lines right after it. Each line gives the kind (dir, file, link, other), a size for regular files and the name; at every level directories come first, then files and links, each sorted by name, and a symbolic link is named but never entered, so a listing cannot leave the directories this session works in. Every listing is bounded — it renders at most %d entries, one line is at most %d bytes and a listing that goes deeper examines at most %d entries — and when it stops at one of those caps it says which cap it reached and that the remaining entries were not examined, so a listing that reports a cap has not seen the whole tree.`, fileread.DefaultListDepth, fileread.MaxListDepth, fileread.DefaultListDepth, fileread.DefaultListEntries, fileread.DefaultListLineBytes, fileread.DefaultListScanned)
	return &schema.ToolInfo{Name: ListDirToolName, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(listDirSchema())}
}

// readFileInfo is the exact public schema of the file tool. The description
// names the read root so the model does not have to guess what a relative path
// is relative to; it never carries the absolute host path.
func readFileInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: ReadFileToolName, Desc: "Read a text file from one of the directories this session works in using the active local Luna subprocess plugin. The path must be relative to one of the directories this session works in (a single configured root when it works in none); an absolute path or one outside them is refused. A whole-file read above the size limit, or a read containing binary NUL bytes, is refused rather than silently truncated. For a large text file, use start_line and max_lines to request a bounded range; the result identifies returned lines and remaining lines. Report these limits and continue from the stated range instead of claiming the entire file was read.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(readFileSchema())}
}

// SearchFilesTool is the model-visible search tool. It is the third
// member of the file family: the wrapper refuses a malformed call, the host
// refuses a path the read root does not hold, a query it cannot search and a mode
// it does not have, and the plugin searches only what the host resolved, in lines
// of the text it can read.
type SearchFilesTool struct{ searcher FileSearcher }

func NewSearchFilesTool(s FileSearcher) *SearchFilesTool { return &SearchFilesTool{searcher: s} }
func (t *SearchFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return searchFilesInfo(), nil
}

// searchFilesSchema is deliberately three parameters: where to start, the text to
// find, and how to read that text. The mode is an explicit choice with a stated
// default rather than an inference from what the query looks like, because the
// same three characters are a literal on one call and a pattern on another; a
// depth, a glob or a filter would let one call widen itself past a bounded search,
// so the schema offers no way to ask for one.
func searchFilesSchema() *jsonschema.Schema {
	type args struct {
		Path  string `json:"path" jsonschema_description:"Directory to search below, or one file to search, relative to one of the directories this session works in; use \".\" for the first of them"`
		Query string `json:"query" jsonschema_description:"Text to find inside single lines. Read as literal text unless mode asks for a pattern, so a dot is a dot by default"`
		Mode  string `json:"mode,omitempty" jsonschema:"enum=literal,enum=regex,default=literal" jsonschema_description:"How to read query: \"literal\" (the default, and what omitting this argument means) matches it exactly as written, and \"regex\" compiles it as an RE2 regular expression matched against each line separately. Any other value is refused"`
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
		Mode  string `json:"mode"`
	}
	err := decodeOne(arguments, &in)
	if err == nil && in.Path == "" {
		err = fmt.Errorf("path is required")
	}
	if err == nil && in.Query == "" {
		err = fmt.Errorf("query is required: a search needs text to look for")
	}
	if err != nil {
		return refuse(ctx, SearchFilesToolName, pluginhost.Output{}, err, startedAt)
	}
	// As with a read or a listing, the requested path is passed through
	// unchanged: the host resolves and validates it against the read root before
	// any plugin sees it, and the query and the mode are validated there too. An
	// unknown mode is refused by the host by name, so the wrapper does not
	// substitute the default for it here — a call that asked for a mode the
	// search does not have is answered as a refusal, never as a literal search.
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: SearchFilesToolName, Summary: "read project files", Target: in.Path, ReadRoots: roots(ctx), Permissions: []plugin.AccessKind{plugin.AccessRead}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, SearchFilesToolName, stopped, pluginhost.Output{}, startedAt)
		}
		return refuse(ctx, SearchFilesToolName, pluginhost.Output{}, err, startedAt)
	}
	out, err := t.searcher.SearchFiles(ctx, pluginhost.SearchRequest{Path: in.Path, Query: in.Query, Mode: in.Mode, Roots: roots(ctx)})
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
// states the four things a model gets wrong about a search: the query is read as
// literal text unless the call asks for the regex mode — and the default is named
// rather than left to be inferred — the path says where the search starts and is
// relative to the read root, a search looks below that path but is bounded and
// says so when it stops, and a hit names its file relative to the path that was
// searched. It never carries the absolute host path.
func searchFilesInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: SearchFilesToolName, Desc: "Search one of the directories this session works in for text, using the active local Luna subprocess plugin. path and query are required; mode is optional and defaults to \"literal\". path says where the search starts — a directory to search below, or one file — and must be relative to one of the directories this session works in (a single configured root when it works in none), where \".\" means the first of them; an absolute path, a path outside them, or a path that is neither a file nor a directory is refused. query is the text to find, matched inside single lines: with mode \"literal\", or with mode omitted, it is matched exactly as written and nothing in it is interpreted, so a regular expression is searched as its own characters and a dot is a dot. With mode \"regex\" the same query is compiled as an RE2 regular expression — Go's regexp is a linear-time engine, so a pattern cannot make the call run away — and matched against each line separately, so a pattern never matches across a line break, and ^ and $ anchor to the ends of the line being tested; a query that is not a valid regular expression is refused rather than searched, and the result of a refused pattern says so instead of reporting no matches. Any other mode value is refused rather than read as the default. A directory is walked below itself in path order, and a symbolic link is never followed, so a search cannot leave them. Each result line is <path>:<line>: <line text>, with <path> relative to the path that was searched, and the result says which mode read the query. A search is bounded — how many matches are rendered, how long one line may be, how many files are read and how large a file may be all have caps — and when it stops at one it says which cap it reached and that the remaining paths were not searched, so a result that reports a cap has not seen the whole tree; content that is not text is counted rather than searched.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(searchFilesSchema())}
}

// FindFilesTool is the model-visible name-search tool. It is the fourth member
// of the file family: the wrapper refuses a malformed call, the host refuses a
// path the roots do not hold and a pattern it cannot evaluate, and the plugin
// walks only what the host resolved, matching a glob against entry names rather
// than reading any content.
type FindFilesTool struct{ finder FileFinder }

func NewFindFilesTool(f FileFinder) *FindFilesTool { return &FindFilesTool{finder: f} }
func (t *FindFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return findFilesInfo(), nil
}

// findFilesSchema is deliberately two parameters: where to look, and the glob
// to match against entry names. A depth, a type filter or a content knob would
// let one call ask a different question than the one the tool answers — which
// entries are called this — so the schema offers no way to ask for one.
func findFilesSchema() *jsonschema.Schema {
	type args struct {
		Path    string `json:"path" jsonschema_description:"Directory to look below, or one file to look at, relative to one of the directories this session works in; use \".\" for the first of them"`
		Pattern string `json:"pattern" jsonschema_description:"Glob matched against one entry name and anchored to the whole name: * matches any run of characters, ? matches one character, [abc] matches one character from a set; a dot is a dot, and there is no other pattern syntax"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"path", "pattern"}
	return s
}

func (t *FindFilesTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var raw any
	if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
		raw = arguments
	}
	startedAt := time.Now()
	emit(ctx, Event{Type: "tool.started", Data: ToolStarted{RunID: runID(ctx), Name: FindFilesToolName, Arguments: raw}})
	var in struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}
	err := decodeOne(arguments, &in)
	if err == nil && in.Path == "" {
		err = fmt.Errorf("path is required")
	}
	if err == nil && in.Pattern == "" {
		err = fmt.Errorf("pattern is required: a find needs a name to match")
	}
	if err != nil {
		return refuse(ctx, FindFilesToolName, pluginhost.Output{}, err, startedAt)
	}
	// As with a read, a listing or a literal search, the requested path is
	// passed through unchanged: the host resolves and validates it against the
	// roots of this run before any plugin sees it, and the pattern is validated
	// there too.
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: FindFilesToolName, Summary: "read project files", Target: in.Path, ReadRoots: roots(ctx), Permissions: []plugin.AccessKind{plugin.AccessRead}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, FindFilesToolName, stopped, pluginhost.Output{}, startedAt)
		}
		return refuse(ctx, FindFilesToolName, pluginhost.Output{}, err, startedAt)
	}
	out, err := t.finder.FindFiles(ctx, pluginhost.FindRequest{Path: in.Path, Pattern: in.Pattern, Roots: roots(ctx)})
	if err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return stopCall(ctx, FindFilesToolName, stopped, out, startedAt)
		}
		return refuse(ctx, FindFilesToolName, out, err, startedAt)
	}
	emit(ctx, Event{Type: "tool.finished", Data: ToolFinished{RunID: runID(ctx), Name: FindFilesToolName, Result: out.Result, DurationMS: callDurationMS(startedAt), Generation: out.Generation, Version: out.Version, PluginPID: out.PluginPID}})
	// Only the rendered paths are model-visible; the serving generation,
	// version and process identity stay in the event stream for the UI.
	return out.Result, nil
}

// findFilesInfo is the exact public schema of the name-search tool. The
// description states the three things a model gets wrong about a name search:
// that the pattern is a glob anchored to the whole name rather than a substring
// or a regular expression, that the path says where to look and is relative to
// the directories the session works in, and that the result is bounded and
// names its own limit. It never carries the absolute host path.
func findFilesInfo() *schema.ToolInfo {
	return &schema.ToolInfo{Name: FindFilesToolName, Desc: "Find the entries whose name matches a pattern in one of the directories this session works in, using the active local Luna subprocess plugin. Both arguments are required, and this finds names, not text. path says where to look — a directory to look below, or one file to look at — and must be relative to one of the directories this session works in (a single configured root when it works in none), where \".\" means the first of them; an absolute path, a path outside them, or a path that is neither a file nor a directory is refused. pattern is a glob over one entry name, anchored to the whole name rather than tested as a substring, so \"main.go\" matches only main.go, \"*_test.go\" matches every Go test file at every depth and \"main?go\" matches a five-character name; * matches any run of characters, ? matches one character and [abc] matches one character from a set, and nothing else is interpreted, so a dot is a dot and a regular expression is matched as its own characters. Every entry below the path is examined, directories among them; a matched directory is rendered with a trailing slash, so it is not mistaken for a file to read. Each line is <kind> <size> <path>, with <path> relative to the path that was looked at. A symbolic link is named when its own name matches but is never entered, so a find cannot leave the directories this session works in. The find is bounded — how many paths are rendered, how long one line may be and how many entries are examined all have caps — and when it stops at one it says which cap it reached and that the remaining entries were not examined, so a result that reports a cap has not seen the whole tree.", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(findFilesSchema())}
}

var (
	_ tool.InvokableTool = (*TextTransformTool)(nil)
	_ tool.InvokableTool = (*ReadFileTool)(nil)
	_ tool.InvokableTool = (*ListDirTool)(nil)
	_ tool.InvokableTool = (*SearchFilesTool)(nil)
	_ tool.InvokableTool = (*FindFilesTool)(nil)
)

type Runner struct {
	history    History
	transcript Transcript
	// capabilities is the registry of enabled contributions. The runner
	// assembles their tools and context blocks; it knows nothing about what any
	// of them means.
	capabilities *plugin.Registry

	// Everything below is the build recipe. It is kept because the model-visible
	// tool set is fixed when the agent is constructed, while the capability list
	// can change at any time (the browser's enable/disable buttons do exactly
	// that). A runner holding only the built agent would keep offering a
	// disabled capability's tool — the capability would read as off everywhere
	// and still be callable, which for a store-backed capability means writing.
	// So the recipe stays here and the agent is rebuilt when the list it was
	// built from is no longer current.
	//
	// The build recipe also carries the model a run is sent to, for the same
	// reason: which model a session asked for is stated per session, while the
	// agent is built with one model fixed inside it.
	mu       sync.Mutex
	buildCtx context.Context
	invoker  Invoker
	files    FileTools
	// cfg is the model list a run may be sent to, its default first.
	cfg config.Config
	// baseConfig 保留无 ProviderSource 时的全局值，不能被某会话覆盖污染。
	baseConfig config.Config
	// source is where the current provider is read from, when one was supplied.
	// With it, a provider saved while this process runs is used by the next run
	// without a restart; without it, cfg is what the runner was built with and
	// stays that way.
	source ProviderSource
	// sig is the configuration the current agent and its clients were built
	// from. A run re-reads the source and rebuilds when this no longer matches
	// what the source states. Guarded by mu.
	sig configSignature
	// built reports whether an agent and a default client exist at all. A runner
	// built around a source has neither until its first run.
	built bool
	// model is the client the runner was started with: the default entry of
	// cfg. Every run that asks for no particular model, and every run that asks
	// for the default entry by name, is sent here.
	model model.ToolCallingChatModel
	// clients caches the clients built for the other models by name, so a
	// session that switches back and forth builds each one once. Guarded by mu,
	// and emptied whenever the provider changes: a client built for the old
	// endpoint must not answer for the new one.
	clients map[string]model.ToolCallingChatModel
	// clientFor builds the client for one model. It is a field so the
	// construction has one home and so a test can stand a fake in for a
	// provider instead of calling one.
	clientFor clientFactory

	runner     *adk.Runner
	builtModel string
	// builtRevision is the capability list's revision at build time, or zero
	// when the runner was built without a list.
	builtRevision  uint64
	selection      *runconfig.Selection
	builtSelection string
	builtEntries   []plugin.Entry
	builtTools     []string
	// maxIterations is how many model turns one run may take before the agent
	// stops it as a runaway loop. It is resolved when the agent is built, so a
	// run and the error that explains it agree on the number.
	maxIterations int
}

// ProviderSource is where the provider a run should be sent to comes from: the
// configuration as it is now, not as it was when the process started.
//
// It is an interface rather than a value because "which endpoint, key and model
// this installation uses" is a question whose answer can change while the
// process runs — the settings page saves a provider, and the next run has to use
// it. An error means the question could not be answered (a file that cannot be
// read, a key that cannot be resolved), which fails the run that asked.
type ProviderSource interface {
	Current() (config.Config, error)
}

// WithProviderSource supplies the source the runner asks for the current
// configuration at the start of every run.
//
// A runner built without one keeps the configuration it was constructed with:
// that is the shape the tests and any embedding that owns the provider itself
// use, and it is not a second, weaker provider path — it is the absence of one.
func WithProviderSource(src ProviderSource) Option { return func(r *Runner) { r.source = src } }

// configSignature is everything about a configuration that the built agent and
// its clients depend on. It exists so "the provider changed" is one comparison
// instead of a list of fields at every call site, and so a change that does not
// matter does not throw away a working agent. 思考档位在客户端构造时固定，
// 因而也必须纳入签名，否则会话切换档位只会改变界面而不改变请求。
type configSignature struct {
	provider        string
	baseURL         string
	apiKey          string
	model           string
	reasoningEffort string
	models          []string
	maxIterations   int
	runTimeout      time.Duration
}

// signatureOf reduces a configuration to the parts a built agent and its clients
// depend on.
func signatureOf(cfg config.Config) configSignature {
	models := make([]string, 0, len(cfg.Models))
	provider := ""
	for i, entry := range cfg.Models {
		models = append(models, entry.Name)
		if i == 0 {
			provider = entry.Provider
		}
	}
	return configSignature{provider: provider, baseURL: cfg.BaseURL, apiKey: cfg.APIKey, model: cfg.Model, reasoningEffort: cfg.ReasoningEffort, models: models, maxIterations: cfg.MaxIterations, runTimeout: cfg.RunTimeout}
}

func (s configSignature) same(other configSignature) bool {
	return s.provider == other.provider && s.baseURL == other.baseURL && s.apiKey == other.apiKey && s.model == other.model && s.reasoningEffort == other.reasoningEffort &&
		s.maxIterations == other.maxIterations && s.runTimeout == other.runTimeout && slices.Equal(s.models, other.models)
}

// clientFactory builds the model client for one model of the active provider.
// Building one resolves the endpoint and the key that provider is called with; it
// is not a request, and the provider is first spoken to by the run.
//
// The endpoint and the key come from the configuration rather than from the entry
// that names the model, because they are properties of the provider: every model
// the active provider serves is served at the same endpoint, with the same key.
type clientFactory func(ctx context.Context, cfg config.Config, modelName string) (model.ToolCallingChatModel, error)

// DefaultMaxIterations is how many model turns one run may take before the agent
// stops it as a runaway loop.
//
// It is a backstop, not a work budget. A task that reads and searches a
// repository, or that works through a list of sources, legitimately takes tens
// of turns — and one turn may carry several tool calls — so this ceiling sits
// far above what real work costs. What ends a run for a real reason is the run
// deadline and Stop; this is only what stops a model that would otherwise keep
// calling tools forever, and a user who wants a tighter or looser guard sets
// max_iterations.
const DefaultMaxIterations = 64

// MaxIterationsFor resolves the turn ceiling a runner built from cfg enforces:
// what the user configured, or this package's default. It exists so a caller
// that has to report the budget does not restate the default.
func MaxIterationsFor(cfg config.Config) int {
	if cfg.MaxIterations > 0 {
		return cfg.MaxIterations
	}
	return DefaultMaxIterations
}

// explainRunBudget names the budget that ended a run.
//
// Eino reports the turn ceiling as its own sentinel with nothing about where
// the number came from, and a run that ends for a reason the user can change
// has to say which number to change. The classification is by marker rather
// than by message text, so a reworded upstream error still classifies.
func explainRunBudget(err error, maxIterations int) error {
	if err == nil || !errors.Is(err, adk.ErrExceedMaxIterations) {
		return err
	}
	return fmt.Errorf("%w: this run reached its %d-turn budget (max_iterations). A task that needs more turns can be given more: raise max_iterations in the user configuration file or set %s", err, maxIterations, config.MaxIterationsEnv)
}

// WithMaxIterations sets how many model turns one run may take before the agent
// stops it as a runaway loop. A non-positive value keeps the default.
func WithMaxIterations(n int) Option { return func(r *Runner) { r.maxIterations = n } }

// WithConfig supplies the model list a run may be sent to. The first entry is
// the default: a run that names no model, and one that names the first entry,
// both use the client the runner was started with.
func WithConfig(cfg config.Config) Option { return func(r *Runner) { r.cfg = cfg } }

// NewRunner builds the agent and its tool set around a model the caller already
// has. Every model-visible tool is registered here, by the core: the plugin-backed
// wrappers whose host-side half the runner was handed, and one wrapper per tool
// contributed by an enabled capability.
func NewRunner(ctx context.Context, m model.ToolCallingChatModel, invoker Invoker, files FileTools, opts ...Option) (*Runner, error) {
	// buildCtx is the construction context, kept for rebuilds: a rebuild is not
	// part of any single run, so it must not inherit that run's cancellation.
	r := &Runner{buildCtx: ctx, model: m, invoker: invoker, files: files, clients: map[string]model.ToolCallingChatModel{}, clientFor: openAICompatibleClient}
	for _, opt := range opts {
		opt(r)
	}
	if r.maxIterations <= 0 {
		r.maxIterations = DefaultMaxIterations
	}
	r.baseConfig = r.cfg
	if err := r.build(m, r.defaultModelName()); err != nil {
		return nil, err
	}
	r.built, r.sig = true, signatureOf(r.cfg)
	return r, nil
}

// NewProviderRunner builds a runner that asks src for the provider to call at the
// start of every run, instead of being handed a model at construction.
//
// Nothing is built here on purpose. A Luna that has never been configured is a
// normal Luna — the settings page that configures it is served by the same
// process — so a runner that could only be constructed from a working provider
// would make the one thing that fixes the installation unreachable. The first run
// asks the source, reports what is missing if anything is, and builds the agent
// and its clients from what it gets.
func NewProviderRunner(ctx context.Context, src ProviderSource, invoker Invoker, files FileTools, opts ...Option) (*Runner, error) {
	if src == nil {
		return nil, errors.New("a provider source is required")
	}
	r := &Runner{buildCtx: ctx, source: src, invoker: invoker, files: files, clients: map[string]model.ToolCallingChatModel{}, clientFor: openAICompatibleClient}
	for _, opt := range opts {
		opt(r)
	}
	if r.maxIterations <= 0 {
		r.maxIterations = DefaultMaxIterations
	}
	return r, nil
}

// missingProviderError is the sentence a run gets when the installation has no
// usable provider. It names the fields to fill in and where they are written,
// because the person reading it is looking at a settings page.
func missingProviderError(missing []string) error {
	return fmt.Errorf("this Luna has no provider configured yet: %s is unset. Fill it in on the settings page, which writes provider.yaml", strings.Join(missing, ", "))
}

// adopt takes the configuration a run should use from now on: it replaces the
// model list, empties the client cache — those clients were built for the endpoint
// and key that have just been replaced — and rebuilds the default client and the
// agent.
//
// The signature is recorded only once the rebuild succeeded, so a build failure
// fails the run instead of leaving the runner believing it has adopted a
// configuration it could not build.
func (r *Runner) adopt(cfg config.Config) error {
	r.cfg = cfg
	r.maxIterations = MaxIterationsFor(cfg)
	r.clients = map[string]model.ToolCallingChatModel{}
	defaultModel := r.defaultModelName()
	m, err := r.clientFor(r.buildCtx, cfg, defaultModel)
	if err != nil {
		return fmt.Errorf("build the client for the model %q of the provider %q: %w", defaultModel, cfg.ProviderHost, err)
	}
	r.model = m
	if err := r.build(m, defaultModel); err != nil {
		return fmt.Errorf("build the agent for the current provider: %w", err)
	}
	r.built, r.sig = true, signatureOf(cfg)
	return nil
}

// build assembles the model-visible tools and the agent that runs them, from the
// capability list as it is now, and records which revision and which model that
// was.
func (r *Runner) build(m model.ToolCallingChatModel, modelName string) error {
	tools := []tool.BaseTool{NewTextTransformTool(r.invoker), NewReadFileTool(r.files), NewListDirTool(r.files)}
	// The search wrapper is registered exactly for a file capability that serves
	// a search: the core offers the model the plugin-backed tools it can route,
	// and never a tool whose call would fail for want of a host half.
	if searcher, ok := r.files.(FileSearcher); ok {
		tools = append(tools, NewSearchFilesTool(searcher))
	}
	// The find wrapper is registered on the same terms: a file capability that
	// serves a name search gets it, and one that does not is offered no tool
	// whose call would fail for want of a host half.
	if finder, ok := r.files.(FileFinder); ok {
		tools = append(tools, NewFindFilesTool(finder))
	}
	entries := r.selectedEntries()
	for _, contributed := range capabilityToolsFor(entries) {
		tools = append(tools, contributed)
	}
	selectedTools := make([]tool.BaseTool, 0, len(tools))
	names := make([]string, 0, len(tools))
	for _, item := range tools {
		info, err := item.Info(r.buildCtx)
		if err != nil {
			return err
		}
		if r.selection.AllowsTool(info.Name) {
			selectedTools = append(selectedTools, item)
			names = append(names, info.Name)
		}
	}
	tools = selectedTools
	a, err := adk.NewChatModelAgent(r.buildCtx, &adk.ChatModelAgentConfig{Name: "luna", Description: "Personal Luna assistant", Instruction: instruction, GenModelInput: r.modelInputFor(entries), Model: m, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}}, MaxIterations: r.maxIterations})
	if err != nil {
		return err
	}
	r.runner = adk.NewRunner(r.buildCtx, adk.RunnerConfig{Agent: a, EnableStreaming: true})
	r.builtRevision = r.currentRevision()
	r.builtModel = modelName
	r.builtEntries = entries
	r.builtTools = names
	r.builtSelection = selectionSignature(r.selection)
	return nil
}

// defaultModelName is the name of the entry a run that names no model is sent
// to: the first entry of the configured list. A runner built without a list has
// no name for it, which is what the empty string here means.
func (r *Runner) defaultModelName() string {
	if len(r.cfg.Models) == 0 {
		return ""
	}
	return r.cfg.Models[0].Name
}

// modelForRun returns the client a run must be sent to and the name it is known
// by. name is what the run asked for; the empty string, and the default entry's
// own name, both select the client the runner was started with.
//
// A name that is not in the configured list is an error, never a fallback: the
// user asked for a model, and a run answered by a different one would look like
// a switch that took effect. Which model answered is not visible in the answer
// itself, so the wrong model can go unnoticed for a long time.
func (r *Runner) modelForRun(name string) (model.ToolCallingChatModel, string, error) {
	requested := strings.TrimSpace(name)
	def := r.defaultModelName()
	if requested == "" || requested == def {
		return r.model, def, nil
	}
	if client, ok := r.clients[requested]; ok {
		return client, requested, nil
	}
	if _, ok := r.modelEntry(requested); !ok {
		return nil, "", fmt.Errorf("unknown model %q: this Luna can run %s", requested, r.knownModels())
	}
	client, err := r.clientFor(r.buildCtx, r.cfg, requested)
	if err != nil {
		return nil, "", fmt.Errorf("build the client for model %q: %w", requested, err)
	}
	r.clients[requested] = client
	return client, requested, nil
}

// modelEntry finds one entry of the configured model list by name.
func (r *Runner) modelEntry(name string) (config.Model, bool) {
	for _, entry := range r.cfg.Models {
		if entry.Name == name {
			return entry, true
		}
	}
	return config.Model{}, false
}

// knownModels names the models a run may be sent to, for an error message.
func (r *Runner) knownModels() string {
	if len(r.cfg.Models) == 0 {
		return "only the model it was started with: no model list is configured"
	}
	names := make([]string, 0, len(r.cfg.Models))
	for _, entry := range r.cfg.Models {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ", ")
}

// currentRevision is the capability list's revision, or zero when the runner was
// built without one (a runner with no capabilities has nothing to rebuild for).
func (r *Runner) currentRevision() uint64 {
	if r.capabilities == nil {
		return 0
	}
	return r.capabilities.Revision()
}

// agentForRun returns the agent a run should use, rebuilding it first when the
// capability list changed, when the run is sent to a different model, or when the
// provider the source reports is no longer the one the agent was built for.
//
// A run takes the agent it gets here and keeps it to the end: a toggle during a
// run rebuilds the agent for the NEXT run, so an in-flight run is never switched
// out from under itself. The same holds for a model and for the provider:
// switching the session's model mid-run, or saving another provider, does not move
// the run that is already talking to one.
// Rebuilding before the run rather than on the toggle keeps the toggle path free
// of this package's build errors.
//
// An unknown model name is reported here, which fails the run. It is not
// silently replaced by the default: the user would be answered by a model they
// did not choose, and nothing in the answer says so.
func (r *Runner) agentForRun(name string, reasoning *string, selection *runconfig.Selection) (*preparedAgent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selection = selection.Clone()
	if err := r.selection.Validate(); err != nil {
		return nil, err
	}
	selectionChanged := selectionSignature(r.selection) != r.builtSelection
	cfg := r.baseConfig
	if r.source != nil {
		var err error
		cfg, err = r.source.Current()
		if err != nil {
			return nil, fmt.Errorf("read the configured provider: %w", err)
		}
		if len(cfg.Missing) > 0 {
			return nil, missingProviderError(cfg.Missing)
		}
	}
	if reasoning != nil {
		selected, err := config.ParseReasoningEffort(*reasoning)
		if err != nil {
			return nil, err
		}
		cfg.ReasoningEffort = selected
	}
	if r.clientFor == nil && reasoning != nil {
		return nil, errors.New("this runner cannot change reasoning without a client factory")
	}
	if r.clientFor != nil && (!r.built || !signatureOf(cfg).same(r.sig)) {
		if err := r.adopt(cfg); err != nil {
			return nil, err
		}
	}
	m, resolved, err := r.modelForRun(name)
	if err != nil {
		return nil, err
	}
	revisionChanged := r.capabilities != nil && r.capabilities.Revision() != r.builtRevision
	if revisionChanged || resolved != r.builtModel || selectionChanged {
		if err := r.build(m, resolved); err != nil {
			return nil, fmt.Errorf("rebuild the agent after a capability or model change: %w", err)
		}
	}
	return &preparedAgent{runner: r.runner, entries: append([]plugin.Entry{}, r.builtEntries...), snapshot: &runconfig.Snapshot{Selection: r.selection.Clone(), Model: resolved, ReasoningEffort: cfg.ReasoningEffort, Capabilities: entryNames(r.builtEntries), Tools: append([]string{}, r.builtTools...)}}, nil
}

// openAIChatConfig is the client configuration for one model of the active
// provider, called with key. The reasoning level is set only when one was chosen:
// the field is then left out of the request entirely, which is what keeps this
// knob from reaching providers that do not define it. How hard the model thinks is
// a parameter of the run; showing the reasoning it produced is a separate concern
// and does not depend on this being set.
func openAIChatConfig(cfg config.Config, modelName, key string) *openai.ChatModelConfig {
	modelConfig := &openai.ChatModelConfig{APIKey: key, BaseURL: cfg.BaseURL, Model: modelName}
	if cfg.ReasoningEffort != "" {
		modelConfig.ReasoningEffort = openai.ReasoningEffortLevel(cfg.ReasoningEffort)
	}
	return modelConfig
}

// openAICompatibleClient builds the client for one model of the provider the
// configuration names. The endpoint and the key are the provider's, so every model
// it serves is reached the same way; the key is never logged or returned.
func openAICompatibleClient(ctx context.Context, cfg config.Config, modelName string) (model.ToolCallingChatModel, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, fmt.Errorf("the provider api_key is not set")
	}
	return openai.NewChatModel(ctx, openAIChatConfig(cfg, modelName, key))
}

// NewOpenAIRunner builds the agent every run is sent to, from a configuration the
// caller already resolved. The run budgets come from the same place: the turn
// ceiling is the configured one, and zero there means the default this package
// declares.
//
// It is the shape a caller with a fixed configuration uses — the tests, and any
// embedding that owns the provider itself. A running Luna uses NewProviderRunner:
// its provider can change while the process runs.
func NewOpenAIRunner(ctx context.Context, cfg config.Config, invoker Invoker, files FileTools, opts ...Option) (*Runner, error) {
	m, err := openAICompatibleClient(ctx, cfg, cfg.Model)
	if err != nil {
		return nil, err
	}
	return NewRunner(ctx, m, invoker, files, append([]Option{WithConfig(cfg), WithMaxIterations(MaxIterationsFor(cfg))}, opts...)...)
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
	workspace := req.WorkspaceID
	if err := r.transcript.AppendMessage(req.SessionID, store.MessageRecord{RunID: req.RunID, Role: role, Text: text, At: at, WorkspaceID: &workspace}); err != nil {
		return fmt.Errorf("persist %s message: %w", role, err)
	}
	return nil
}

func (r *Runner) appendRun(req RunRequest, startedAt time.Time, status string, usage *store.Usage) error {
	if r.transcript == nil || req.SessionID == "" {
		return nil
	}
	if err := r.transcript.AppendRun(req.SessionID, store.RunRecord{RunID: req.RunID, StartedAt: startedAt, EndedAt: time.Now(), Status: status, Usage: usage, Configuration: req.configuration}); err != nil {
		return fmt.Errorf("persist run record: %w", err)
	}
	return nil
}

func (r *Runner) Run(parent context.Context, req RunRequest) (answer string, err error) {
	startedAt := time.Now()
	req.Setup = req.Setup.Clone()
	if err := req.Setup.Validate(); err != nil {
		return "", err
	}
	if req.Setup != nil {
		if req.Model == "" {
			req.Model = req.Setup.Model
		}
		if req.ReasoningEffort == nil {
			req.ReasoningEffort = req.Setup.ReasoningEffort
		}
	}
	usage := &runUsage{}
	recorder := newRecorder(req.Sink, r.transcript, req.SessionID, req.RunID)
	// The run identity travels in the context so a capability's tools can
	// attribute what they store without being handed the session themselves.
	permissions := plugin.DefaultAccessPolicy()
	if req.Permissions != nil {
		permissions = *req.Permissions
	}
	if !permissions.Valid() {
		return "", fmt.Errorf("invalid run permission policy")
	}
	ctx := plugin.WithRun(WithRoots(WithRun(parent, req.RunID, recorder), req.Roots), plugin.RunInfo{RunID: req.RunID, SessionID: req.SessionID, WorkspaceID: req.WorkspaceID, Selection: req.Setup, ExecutionMode: req.ExecutionMode, Permissions: &permissions, AutomaticWriteDirs: append([]string{}, req.AutomaticWriteDirs...), WriteScopeError: req.WriteScopeError, Approve: req.Approve})
	emit(ctx, Event{Type: "run.started", Data: RunStarted{RunID: req.RunID, SessionID: req.SessionID}})
	defer func() {
		status := runStatus(err)
		if appendErr := r.appendRun(req, startedAt, status, usage.emit(ctx, req.RunID, err == nil)); appendErr != nil && err == nil {
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
	// The agent is resolved once per run: a capability toggled or a model chosen
	// since the last run is built into this one, and this run keeps the agent it
	// started with even if either changes again while it is in flight.
	prepared, err := r.agentForRun(req.Model, req.ReasoningEffort, req.Setup)
	if err != nil {
		return "", err
	}
	req.configuration = prepared.snapshot
	req.configuration.WorkspaceID = req.WorkspaceID
	ctx, err = prepared.freezeResources(ctx)
	if err != nil {
		return "", err
	}
	if req.Configured != nil {
		req.Configured(req.configuration.Clone())
	}
	if req.Setup != nil {
		emit(ctx, Event{Type: "run.configuration", Data: req.configuration.Clone()})
	}
	iter := prepared.runner.Run(ctx, input)
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
			// The turn ceiling is the one iterator error the user can do
			// something about, so it is reported with the number to change.
			return "", explainRunBudget(ev.Err, r.maxIterations)
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput
		if mv.Role != schema.Assistant {
			continue
		}
		if mv.IsStreaming {
			usage.begin()
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
				if meta := chunk.ResponseMeta; meta != nil {
					usage.observe(meta.Usage)
				}
			}
			sr.Close()
			usage.finish(ctx, req.RunID)
			if !hasToolCalls {
				b.WriteString(turn.String())
			}
		} else if mv.Message != nil {
			usage.begin()
			if mv.Message.ResponseMeta != nil {
				usage.observe(mv.Message.ResponseMeta.Usage)
			}
			usage.finish(ctx, req.RunID)
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
