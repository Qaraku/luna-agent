package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

// 模型可见的默认工具名。运行时只能选择已登记的工具，不能提供源码或可执行路径。
const (
	ToolTextTransform = "luna_text_transform"
	ToolReadFile      = "luna_read_file"
	ToolListDir       = "luna_list_dir"
	ToolSearchFiles   = "luna_search_files"
	ToolFindFiles     = "luna_find_files"
)

// ToolSpec 是可信装配方登记的工具及其 plugins/ 内源码目录，不接受浏览器构建路径。
type ToolSpec struct {
	Tool string
	Dir  string
	// Lazy 的可选进程在第一次调用或显式重载时启动，不阻塞其他能力启动。
	Lazy bool
}

// Allowlist 是根应用的默认工具注册清单；版本由每个插件报告，不是宿主的选择枚举。
var Allowlist = []ToolSpec{
	{Tool: ToolTextTransform, Dir: "text_transform"},
	{Tool: ToolReadFile, Dir: "read_file"},
	{Tool: ToolListDir, Dir: "list_dir"},
	{Tool: ToolSearchFiles, Dir: "search_files"},
	{Tool: ToolFindFiles, Dir: "find_files"},
}

var toolNamePattern = regexp.MustCompile("^[a-zA-Z][a-zA-Z0-9_-]{0,63}$")

// Infrastructure failures mean the tool never ran because its owned plugin
// could not serve the call. They end the run, unlike a refusal the tool makes
// about the call itself: internal/agent turns those into a result the model can
// read and explain. Each one is a sentinel so that decision is a classification
// rather than a message match.
var (
	// ErrUnknownTool reports a tool name outside the allowlist.
	ErrUnknownTool = errors.New("unknown tool")
	// ErrNoActivePlugin reports a tool with no active generation: the host is
	// closed, or every generation it started has failed.
	ErrNoActivePlugin = errors.New("no active plugin")
	// ErrRPCTimeout reports a call that outlived RPCTimeout; the owned plugin
	// was terminated.
	ErrRPCTimeout = errors.New("plugin RPC timeout")
	// ErrRPCCanceled reports a call whose run context ended; the owned plugin
	// was terminated.
	ErrRPCCanceled = errors.New("plugin RPC canceled")
	// ErrPluginGone reports a plugin process that died mid-call.
	ErrPluginGone = errors.New("plugin process is gone")
)

type Input = pluginprotocol.Input
type Output struct {
	Result     string `json:"result"`
	Generation uint64 `json:"generation"`
	Version    string `json:"version"`
	PluginPID  int    `json:"plugin_pid"`
}
type Record struct {
	Tool       string `json:"tool"`
	Generation uint64 `json:"generation"`
	Version    string `json:"version"`
	PluginPID  int    `json:"plugin_pid"`
	Status     string `json:"status"`
	Inflight   int    `json:"inflight"`
}
type Event struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}
type State struct {
	Tools   []string `json:"tools"`
	Plugins []Record `json:"plugins"`
	Events  []Event  `json:"events"`
}

// Active returns the record of the generation currently serving tool, or nil
// when that tool has no published generation.
func (s State) Active(tool string) *Record {
	for i := range s.Plugins {
		if s.Plugins[i].Tool == tool && s.Plugins[i].Status == "active" {
			return &s.Plugins[i]
		}
	}
	return nil
}

type Options struct {
	// Prebuilt非nil表示分发模式，所有登记工具必须有固定二进制；不回退源码构建。
	Prebuilt map[string]PrebuiltBinary
	// RuntimeDir存放可回收的插件代次；空值仅保留源码运行的旧路径。
	RuntimeDir string
	// Tools 可由可信装配方提供；nil 使用默认清单。每项在启动前验证并复制。
	Tools        []ToolSpec
	BuildTimeout time.Duration
	StartTimeout time.Duration
	RPCTimeout   time.Duration
	// ReadRoot bounds luna_read_file, luna_list_dir, luna_search_files and
	// luna_find_files. It
	// is the default root: a call that names no roots of its own is checked
	// against it, and it defaults to the repository root. It is resolved once,
	// at construction, so the containment check compares real directories
	// rather than symbolic links.
	ReadRoot string
	// ReadLimit is the single-read size cap in bytes. It defaults to
	// fileread.DefaultLimit (256 KiB).
	ReadLimit int
	// ListMaxEntries, ListMaxLineBytes and ListMaxScanned are the three
	// listing caps: at most this many entries are rendered, one rendered line
	// is at most this many bytes, and at most this many directory entries are
	// examined by a listing that goes below the directory it was given. They
	// default to fileread.DefaultListEntries, fileread.DefaultListLineBytes
	// and fileread.DefaultListScanned, and each is stated in the result when
	// it is reached.
	ListMaxEntries   int
	ListMaxLineBytes int
	ListMaxScanned   int
	// SearchMaxMatches, SearchMaxLineBytes, SearchMaxFiles and
	// SearchMaxFileBytes are the four search caps: at most this many matching
	// lines are rendered, one rendered line is at most this many bytes, at most
	// this many files are read, and no file larger than this many bytes is read.
	// They default to the matching fileread.DefaultSearch* values, and every cap
	// that is reached is stated in the result.
	SearchMaxMatches   int
	SearchMaxLineBytes int
	SearchMaxFiles     int
	SearchMaxFileBytes int
	// FindMaxPaths, FindMaxLineBytes and FindMaxScanned are the three name-search
	// caps: at most this many matching paths are rendered, one rendered line is
	// at most this many bytes, and at most this many directory entries are
	// examined. They default to the matching fileread.DefaultFind* values, and
	// every cap that is reached is stated in the result. The scan cap is larger
	// than the search's file cap because a name search reads no file content.
	FindMaxPaths     int
	FindMaxLineBytes int
	FindMaxScanned   int
}

// ReadRequest is a file-read request from the core. Path is the raw,
// model-supplied path: the host validates it and the plugin never sees it.
type ReadRequest struct {
	Path string
	// Roots are the directories this call may read from, in the order they are
	// tried: the first root that holds Path wins. They are the working
	// directories of the run that produced the call — a session's workspace —
	// and they come from the core, never from the model. An empty list means
	// the host's own ReadRoot, so a call from a run that names no working
	// directories is checked exactly as it was before Roots existed. They are
	// a range and not a permission: a root that is not an absolute directory
	// can only make a call refuse, because no path is contained in one.
	Roots []string
	// StartLine and MaxLines are the optional line range of the read, passed
	// through from the call and validated here: StartLine is the 1-based
	// number of the first line to return and MaxLines the largest number of
	// lines to return, and 0 means the call named neither. A negative value is
	// refused before any work is done. A range read is also why a path may be
	// a file that is over the read limit: the limit bounds what comes back,
	// and being able to read a part of a file that cannot be read whole is the
	// point of naming a range.
	StartLine int
	MaxLines  int
	// DelayMS is an operations and test knob for holding an RPC in flight. It
	// is never exposed to the model.
	DelayMS int
}

// ListRequest is a directory-listing request from the core. Path is the raw,
// model-supplied path, on the same terms as ReadRequest: the host validates it
// against the roots the call names and the plugin only ever sees the resolved
// absolute path.
//
// Depth is how many levels below that path the listing covers. Zero means the
// call named none, which is fileread.DefaultListDepth — one level, the listing
// this tool has always been — and anything else has to be inside
// fileread.DefaultListDepth..fileread.MaxListDepth or the call is refused here.
// It is a bound and not a widening: the depth says how far down a listing looks,
// the listing is still capped on how much it renders and on how much it examines
// (see Options.ListMaxScanned), and a listing that stopped at a cap says so.
type ListRequest struct {
	Path string
	// Depth is the number of levels to cover, or zero for the default of one.
	Depth int
	// Roots are the directories this call may list from, on the same terms as
	// ReadRequest.Roots.
	Roots []string
	// DelayMS is an operations and test knob for holding an RPC in flight. It
	// is never exposed to the model.
	DelayMS int
}

// SearchRequest is a search request from the core. Path is the raw,
// model-supplied path, on the same terms as ReadRequest and ListRequest: the
// host validates it against the roots the call names and the plugin only ever
// sees the resolved absolute path — of a file or of a directory. Query is the
// raw, model-supplied query, validated here as well, and Mode says how it is to
// be read: empty (the default when the call named none) or "literal" matches it
// as data, and "regex" asks for it to be read as an RE2 pattern. Mode is part of
// the query's meaning rather than a knob that widens the search, so a value the
// search does not have is refused here rather than read as the default; there is
// deliberately no depth or filter knob, because a search walks below the path it
// was given and stops at its caps, and no request can widen that.
type SearchRequest struct {
	Path  string
	Query string
	// Mode is the raw, model-supplied mode. See fileread.ModeLiteral and
	// fileread.ModeRegex for the two values, and fileread.ValidateMode for what
	// an unnamed mode means.
	Mode string
	// Roots are the directories this call may search, on the same terms as
	// ReadRequest.Roots.
	Roots []string
	// DelayMS is an operations and test knob for holding an RPC in flight. It
	// is never exposed to the model.
	DelayMS int
}

// FindRequest is a name-search request from the core. Path is the raw,
// model-supplied path, on the same terms as ReadRequest, ListRequest and
// SearchRequest: the host validates it against the roots the call names and the
// plugin only ever sees the resolved absolute path — of a file or of a
// directory. Pattern is the raw, model-supplied name pattern, validated here as
// well. There is deliberately no depth or type filter knob: a find walks below
// the path it was given, matches every entry name it sees and stops at its
// caps, and no request can widen that.
type FindRequest struct {
	Path    string
	Pattern string
	// Roots are the directories this call may find in, on the same terms as
	// ReadRequest.Roots.
	Roots []string
	// DelayMS is an operations and test knob for holding an RPC in flight. It
	// is never exposed to the model.
	DelayMS int
}

type generation struct {
	rt     *toolRuntime
	record Record
	client *plugin.Client
	tool   pluginprotocol.Tool
	path   string
	stop   sync.Once
}

// toolRuntime holds the generations of one allowlisted tool. Each tool has its
// own active generation, so replacing one tool never disturbs another.
type toolRuntime struct {
	spec     ToolSpec
	active   *generation
	retiring []*generation
}

type Host struct {
	mu       sync.Mutex
	reloadMu sync.Mutex
	next     uint64
	root     string
	opts     Options
	tools    []*toolRuntime
	events   []Event
	closed   bool
}

func (h *Host) withDefaults() {
	o := h.opts
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = 60 * time.Second
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 5 * time.Second
	}
	if o.RPCTimeout <= 0 {
		o.RPCTimeout = 5 * time.Second
	}
	if o.ReadLimit <= 0 {
		o.ReadLimit = fileread.DefaultLimit
	}
	if o.ListMaxEntries <= 0 {
		o.ListMaxEntries = fileread.DefaultListEntries
	}
	if o.ListMaxLineBytes <= 0 {
		o.ListMaxLineBytes = fileread.DefaultListLineBytes
	}
	if o.ListMaxScanned <= 0 {
		o.ListMaxScanned = fileread.DefaultListScanned
	}
	if o.SearchMaxMatches <= 0 {
		o.SearchMaxMatches = fileread.DefaultSearchMatches
	}
	if o.SearchMaxLineBytes <= 0 {
		o.SearchMaxLineBytes = fileread.DefaultSearchLineBytes
	}
	if o.SearchMaxFiles <= 0 {
		o.SearchMaxFiles = fileread.DefaultSearchFiles
	}
	if o.SearchMaxFileBytes <= 0 {
		o.SearchMaxFileBytes = fileread.DefaultSearchFileBytes
	}
	if o.FindMaxPaths <= 0 {
		o.FindMaxPaths = fileread.DefaultFindPaths
	}
	if o.FindMaxLineBytes <= 0 {
		o.FindMaxLineBytes = fileread.DefaultFindLineBytes
	}
	if o.FindMaxScanned <= 0 {
		o.FindMaxScanned = fileread.DefaultFindEntries
	}
	if o.ReadRoot == "" {
		o.ReadRoot = h.root
	}
	h.opts = o
}

// resolveReadRoot fails fast on an unusable read root instead of turning every
// read into a not-found error later.
func (h *Host) resolveReadRoot() error {
	absolute, err := filepath.Abs(h.opts.ReadRoot)
	if err != nil {
		return fmt.Errorf("read root %q: %w", h.opts.ReadRoot, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("read root %q is not usable: %w", h.opts.ReadRoot, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("read root %q is not usable: %w", h.opts.ReadRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("read root %q is not a directory", h.opts.ReadRoot)
	}
	h.opts.ReadRoot = resolved
	return nil
}

// New 启动已登记的真实源码实现。任一启动失败会清理已启动进程，不返回半可用宿主。
func New(ctx context.Context, root string, opts Options) (*Host, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	h := &Host{root: resolved, opts: opts, next: 1}
	h.withDefaults()
	if opts.Prebuilt != nil {
		h.opts.Prebuilt = make(map[string]PrebuiltBinary, len(opts.Prebuilt))
		for name, binary := range opts.Prebuilt {
			h.opts.Prebuilt[name] = binary
		}
		if !filepath.IsAbs(opts.RuntimeDir) {
			return nil, fmt.Errorf("prebuilt runtime directory must be absolute")
		}
	}
	if err := h.resolveReadRoot(); err != nil {
		return nil, err
	}
	specs := opts.Tools
	if specs == nil {
		specs = Allowlist
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		if !toolNamePattern.MatchString(spec.Tool) || seen[spec.Tool] {
			return nil, fmt.Errorf("invalid or duplicate registered tool %q", spec.Tool)
		}
		seen[spec.Tool] = true
		if h.opts.Prebuilt != nil {
			binary, ok := h.opts.Prebuilt[spec.Tool]
			if !ok || binary.validate() != nil {
				return nil, fmt.Errorf("missing or invalid prebuilt tool %s", spec.Tool)
			}
		} else if _, err := fileread.ResolveDir(filepath.Join(h.root, "plugins"), spec.Dir); err != nil && !(spec.Lazy && errors.Is(err, fileread.ErrNotFound)) {
			return nil, fmt.Errorf("source for %s: %w", spec.Tool, err)
		}
		h.tools = append(h.tools, &toolRuntime{spec: spec})
	}
	for _, rt := range h.tools {
		if rt.spec.Lazy {
			continue
		}
		g, err := h.start(ctx, rt)
		if err != nil {
			h.Close()
			return nil, err
		}
		g.record.Generation = h.next
		g.record.Status = "active"
		rt.active = g
		h.event("activated", fmt.Sprintf("%s generation %d ready", rt.spec.Tool, g.record.Generation))
	}
	return h, nil
}
func minimalEnv() []string {
	out := []string{}
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE"} {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
func watchStartup(ctx context.Context, kill func()) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(exited)
		select {
		case <-done:
		case <-ctx.Done():
			kill()
		}
	}()
	return func() { once.Do(func() { close(done) }); <-exited }
}
func (h *Host) start(parent context.Context, rt *toolRuntime) (*generation, error) {
	path, err := h.prepareExecutable(parent, rt.spec)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	dir := rt.spec.Dir
	proc := exec.Command(path)
	proc.Env = minimalEnv()
	client := plugin.NewClient(&plugin.ClientConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{}}, Cmd: proc, SkipHostEnv: true, AllowedProtocols: []plugin.Protocol{plugin.ProtocolNetRPC}, StartTimeout: h.opts.StartTimeout, Logger: hclog.NewNullLogger(), SyncStdout: io.Discard, SyncStderr: io.Discard})
	startCtx, cancel := context.WithTimeout(parent, h.opts.StartTimeout)
	defer cancel()
	stopWatch := watchStartup(startCtx, client.Kill)
	defer stopWatch()
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("handshake %s: %w", dir, err)
	}
	raw, err := rpcClient.Dispense("tool")
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("dispense %s: %w", dir, err)
	}
	pt, valid := raw.(pluginprotocol.Tool)
	if !valid {
		client.Kill()
		return nil, fmt.Errorf("invalid tool interface")
	}
	meta, err := pt.Metadata()
	if err != nil || strings.TrimSpace(meta.Version) == "" || len(meta.Version) > 128 || strings.ContainsAny(meta.Version, "\r\n\x00") || meta.Protocol != 1 || proc.Process == nil || meta.PID != proc.Process.Pid {
		client.Kill()
		return nil, fmt.Errorf("invalid plugin metadata for %s", dir)
	}
	stopWatch()
	if err := startCtx.Err(); err != nil {
		client.Kill()
		return nil, fmt.Errorf("startup deadline: %w", err)
	}
	ok = true
	return &generation{rt: rt, record: Record{Tool: rt.spec.Tool, Version: meta.Version, PluginPID: meta.PID}, client: client, tool: pt, path: path}, nil
}
func (h *Host) event(kind, msg string) {
	h.events = append(h.events, Event{Time: time.Now(), Type: kind, Message: msg})
	if len(h.events) > 100 {
		h.events = h.events[len(h.events)-100:]
	}
}
func (h *Host) runtime(tool string) *toolRuntime {
	for _, rt := range h.tools {
		if rt.spec.Tool == tool {
			return rt
		}
	}
	return nil
}
func (h *Host) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := State{Tools: []string{}, Plugins: []Record{}, Events: append([]Event{}, h.events...)}
	for _, rt := range h.tools {
		s.Tools = append(s.Tools, rt.spec.Tool)
		if rt.active != nil {
			s.Plugins = append(s.Plugins, rt.active.record)
		}
		for _, g := range rt.retiring {
			s.Plugins = append(s.Plugins, g.record)
		}
	}
	return s
}

// InvokeText 为能力提供不带文件路径的文本 RPC。工具身份来自可信能力实现，
// Text/Mode 只是数据，不参与构建命令；文件工具仍必须走自己的宿主路径校验。
func (h *Host) InvokeText(ctx context.Context, name, text, mode string) (Output, error) {
	if len(text) > 16384 || len(mode) > 32 {
		return Output{}, fmt.Errorf("text max 16384 bytes; mode max 32 bytes")
	}
	h.mu.Lock()
	lazy := false
	for _, rt := range h.tools {
		if rt.spec.Tool == name {
			lazy = rt.spec.Lazy && rt.active == nil
			break
		}
	}
	h.mu.Unlock()
	if lazy {
		if err := h.Reload(ctx, name); err != nil {
			return Output{}, fmt.Errorf("%w: %v", ErrNoActivePlugin, err)
		}
	}
	return h.invoke(ctx, name, Input{Text: text, Mode: mode})
}

// Invoke calls the text-transform tool.
func (h *Host) Invoke(ctx context.Context, in Input) (Output, error) {
	if len(in.Text) > 16384 || in.DelayMS < 0 || in.DelayMS > pluginprotocol.MaxDelayMS {
		return Output{}, fmt.Errorf("text max 16384 bytes; delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	return h.invoke(ctx, ToolTextTransform, in)
}

// ReadFile calls the file-read tool. The requested path is validated here, on
// the host side, against the roots this call names; the plugin is handed only
// the resolved absolute path, the cap and the optional line range.
//
// Two things the call may ask for change what the path check means. The two
// range counters are validated first, so a negative one is refused before any
// path is resolved. And a call that names a range resolves its path with
// fileread.ResolveRange, which applies the same normalization, containment and
// symbolic-link checks as fileread.Resolve but does not refuse a file over the
// read limit — reading a part of such a file is exactly what a range is for.
// The limit still bounds the bytes that come back, and the implementation states
// what it left unread.
func (h *Host) ReadFile(ctx context.Context, req ReadRequest) (Output, error) {
	if len(req.Path) > 4096 {
		return Output{}, fmt.Errorf("path must not exceed 4096 bytes")
	}
	if req.DelayMS < 0 || req.DelayMS > pluginprotocol.MaxDelayMS {
		return Output{}, fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	if err := fileread.ValidateRange(req.StartLine, req.MaxLines); err != nil {
		return Output{}, err
	}
	var resolved fileread.Resolved
	var err error
	if req.StartLine > 0 || req.MaxLines > 0 {
		resolved, err = fileread.ResolveRangeInRoots(h.readRoots(req.Roots), req.Path)
	} else {
		resolved, err = fileread.ResolveInRoots(h.readRoots(req.Roots), req.Path, h.opts.ReadLimit)
	}
	if err != nil {
		return Output{}, err
	}
	return h.invoke(ctx, ToolReadFile, Input{Path: resolved.Path, MaxBytes: h.opts.ReadLimit, StartLine: req.StartLine, MaxLines: req.MaxLines, DelayMS: req.DelayMS})
}

// ListDir calls the directory-listing tool. The requested path is validated here,
// on the host side, exactly as a read path is — same normalization, same
// containment, same symbolic-link resolution, against the same roots — and the
// plugin is handed only the resolved absolute directory path plus the depth and
// the three caps.
//
// A refusal (an absolute path, a `..` escape, something that is not a
// directory, a nonexistent path, a depth outside the range the listing has) is
// therefore made before any RPC, so no plugin process ever sees a path none of
// the roots holds or a depth the listing does not have. The depth is checked
// before the path is even resolved, because it is a refusal about the call
// rather than about the filesystem: a call that asked for depth 9 is refused
// whether or not the directory it named exists.
func (h *Host) ListDir(ctx context.Context, req ListRequest) (Output, error) {
	if len(req.Path) > 4096 {
		return Output{}, fmt.Errorf("path must not exceed 4096 bytes")
	}
	if req.DelayMS < 0 || req.DelayMS > pluginprotocol.MaxDelayMS {
		return Output{}, fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	if err := fileread.ValidateDepth(req.Depth); err != nil {
		return Output{}, err
	}
	resolved, err := fileread.ResolveDirInRoots(h.readRoots(req.Roots), req.Path)
	if err != nil {
		return Output{}, err
	}
	return h.invoke(ctx, ToolListDir, Input{Path: resolved.Path, Depth: fileread.DepthOrDefault(req.Depth), MaxEntries: h.opts.ListMaxEntries, MaxLineBytes: h.opts.ListMaxLineBytes, MaxScanned: h.opts.ListMaxScanned, DelayMS: req.DelayMS})
}

// SearchFiles calls the search tool. The requested path is validated here, on
// the host side, exactly as a read or a listing path is — same normalization,
// same containment, same symbolic-link resolution, through
// fileread.ResolveSearchInRoots, against the same roots — and so are the query
// and the mode, which are refused here if the query is empty or too long or if
// the mode is one the search does not have, rather than reaching a plugin that
// would have to invent an answer. The plugin is handed only the resolved absolute
// path, the query, the mode and the four caps.
//
// A refusal (an absolute path, a `..` escape, a path outside every root,
// something that exists as neither a file nor a directory, an empty query, an
// unknown mode) is therefore made before any RPC, so no plugin process ever sees
// a path none of the roots holds. Where the search may look afterwards is bounded
// by the plugin's walk, which follows no symbolic link at all, so the search
// cannot leave a root by another route either. Whether the query compiles as a
// pattern is checked on the plugin side, once, by the same fileread search the
// host validated the mode against; the host does not compile it a second time.
func (h *Host) SearchFiles(ctx context.Context, req SearchRequest) (Output, error) {
	if len(req.Path) > 4096 {
		return Output{}, fmt.Errorf("path must not exceed 4096 bytes")
	}
	if req.DelayMS < 0 || req.DelayMS > pluginprotocol.MaxDelayMS {
		return Output{}, fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	if err := fileread.ValidateMode(req.Mode); err != nil {
		return Output{}, err
	}
	if err := fileread.ValidateQuery(req.Query); err != nil {
		return Output{}, err
	}
	resolved, err := fileread.ResolveSearchInRoots(h.readRoots(req.Roots), req.Path)
	if err != nil {
		return Output{}, err
	}
	return h.invoke(ctx, ToolSearchFiles, Input{Path: resolved.Path, Query: req.Query, Mode: req.Mode, MaxMatches: h.opts.SearchMaxMatches, MaxLineBytes: h.opts.SearchMaxLineBytes, MaxFiles: h.opts.SearchMaxFiles, MaxFileBytes: h.opts.SearchMaxFileBytes, DelayMS: req.DelayMS})
}

// FindFiles calls the name-search tool. The requested path is validated here,
// on the host side, exactly as a read, a listing or a search validates
// one — same normalization, same containment, same symbolic-link resolution,
// through fileread.ResolveSearchInRoots, which is shared with the literal
// search because the requirement is the same one: the starting path must be one
// existing file or one existing directory, and never a symbolic link — and so
// is the pattern, which is refused here if it is empty, too long, addressed at a
// path rather than at a name, or not a glob the find can evaluate. The plugin is
// handed only the resolved absolute path, the pattern and the three caps.
//
// A refusal (an absolute path, a `..` escape, a path outside every root,
// something that exists as neither a file nor a directory, an empty pattern) is
// therefore made before any RPC, so no plugin process ever sees a path none of
// the roots holds. Where the find may look afterwards is bounded by the
// plugin's walk, which follows no symbolic link at all, so it cannot leave a
// root by another route either.
func (h *Host) FindFiles(ctx context.Context, req FindRequest) (Output, error) {
	if len(req.Path) > 4096 {
		return Output{}, fmt.Errorf("path must not exceed 4096 bytes")
	}
	if req.DelayMS < 0 || req.DelayMS > pluginprotocol.MaxDelayMS {
		return Output{}, fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	if err := fileread.ValidatePattern(req.Pattern); err != nil {
		return Output{}, err
	}
	resolved, err := fileread.ResolveSearchInRoots(h.readRoots(req.Roots), req.Path)
	if err != nil {
		return Output{}, err
	}
	return h.invoke(ctx, ToolFindFiles, Input{Path: resolved.Path, Pattern: req.Pattern, MaxPaths: h.opts.FindMaxPaths, MaxLineBytes: h.opts.FindMaxLineBytes, MaxScanned: h.opts.FindMaxScanned, DelayMS: req.DelayMS})
}

// readRoots is the set of roots one file call is checked against: the roots the
// caller named for this call, or the host's configured ReadRoot when it named
// none. The default is what keeps a run that works in no particular directory
// reading exactly what it read before roots existed.
//
// Nothing here has to validate a named root against anything: a root that is
// not an absolute existing directory contains no path at all, so withinRoot
// refuses every request against it. A root can therefore only make a call
// refuse, never make one wider than the roots it was given.
func (h *Host) readRoots(roots []string) []string {
	if len(roots) == 0 {
		return []string{h.opts.ReadRoot}
	}
	return roots
}
func (h *Host) invoke(ctx context.Context, tool string, in Input) (Output, error) {
	h.mu.Lock()
	rt := h.runtime(tool)
	if rt == nil {
		h.mu.Unlock()
		return Output{}, fmt.Errorf("%w: %s", ErrUnknownTool, tool)
	}
	if h.closed || rt.active == nil {
		h.mu.Unlock()
		return Output{}, fmt.Errorf("%w for %s", ErrNoActivePlugin, tool)
	}
	g := rt.active
	g.record.Inflight++
	h.mu.Unlock()
	type reply struct {
		v   string
		err error
	}
	done := make(chan reply, 1)
	go func() { v, e := g.tool.Invoke(in); done <- reply{v, e} }()
	timer := time.NewTimer(h.opts.RPCTimeout)
	defer timer.Stop()
	var r reply
	terminated := false
	select {
	case r = <-done:
	case <-timer.C:
		terminated = true
		g.client.Kill()
		r = <-done
		r.err = fmt.Errorf("%w; owned plugin terminated", ErrRPCTimeout)
	case <-ctx.Done():
		terminated = true
		g.client.Kill()
		r = <-done
		r.err = fmt.Errorf("%w; owned plugin terminated: %w", ErrRPCCanceled, ctx.Err())
	}
	if r.err != nil && !terminated && pluginGone(g, r.err) {
		r.err = fmt.Errorf("%w: %s", ErrPluginGone, r.err)
	}
	h.mu.Lock()
	g.record.Inflight--
	if terminated {
		g.record.Status = "failed"
		if rt.active == g {
			rt.active = nil
		}
	}
	drain := g.record.Status != "active" && g.record.Inflight == 0
	h.event("invoke_finished", fmt.Sprintf("%s generation %d completed (error=%v)", tool, g.record.Generation, r.err != nil))
	h.mu.Unlock()
	if drain {
		h.retire(g)
	}
	return Output{Result: r.v, Generation: g.record.Generation, Version: g.record.Version, PluginPID: g.record.PluginPID}, r.err
}

// pluginGone reports whether an RPC error means the owned plugin process is no
// longer there to serve calls. A plugin that refuses a call answers with an
// error of its own and stays alive, so only a dead connection counts here — a
// refusal must not be mistaken for a dead process.
func pluginGone(g *generation, err error) bool {
	if g.client != nil && g.client.Exited() {
		return true
	}
	return errors.Is(err, rpc.ErrShutdown) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// Reload 重建单个已登记工具，target 为空时重建全部；只有全部验证通过才发布。
// 请求只选择注册身份，不参与构建路径。版本号可不变，同版本重建也产生新代次。
func (h *Host) Reload(ctx context.Context, target string) error {
	if !h.reloadMu.TryLock() {
		return fmt.Errorf("reload already in progress")
	}
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("plugin host closed")
	}
	var selected []*toolRuntime
	for _, rt := range h.tools {
		if target == "" || rt.spec.Tool == target {
			selected = append(selected, rt)
		}
	}
	if len(selected) == 0 {
		h.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownTool, target)
	}
	label := target
	if label == "" {
		label = "all registered tools"
	}
	h.event("build_started", label)
	h.mu.Unlock()
	var started []*generation
	cleanup := func() {
		for _, g := range started {
			g.client.Kill()
			_ = os.Remove(g.path)
		}
	}
	for _, rt := range selected {
		g, err := h.start(ctx, rt)
		if err != nil {
			cleanup()
			h.mu.Lock()
			h.event("reload_failed", label+" rejected; active unchanged")
			h.mu.Unlock()
			return err
		}
		started = append(started, g)
	}
	h.mu.Lock()
	if h.closed || ctx.Err() != nil {
		h.mu.Unlock()
		cleanup()
		return fmt.Errorf("reload expired before publish")
	}
	h.next++
	var draining []*generation
	for i, rt := range selected {
		g := started[i]
		g.record.Generation = h.next
		g.record.Status = "active"
		old := rt.active
		rt.active = g
		h.event("activated", fmt.Sprintf("%s generation %d pid %d", rt.spec.Tool, g.record.Generation, g.record.PluginPID))
		if old != nil {
			old.record.Status = "retiring"
			rt.retiring = append(rt.retiring, old)
			if old.record.Inflight == 0 {
				draining = append(draining, old)
			}
		}
	}
	h.mu.Unlock()
	for _, g := range draining {
		h.retire(g)
	}
	return nil
}
func (h *Host) retire(g *generation) {
	g.stop.Do(func() {
		g.client.Kill()
		_ = os.Remove(g.path)
		h.mu.Lock()
		defer h.mu.Unlock()
		if g.rt != nil {
			for i, x := range g.rt.retiring {
				if x == g {
					g.rt.retiring = append(g.rt.retiring[:i], g.rt.retiring[i+1:]...)
					break
				}
			}
		}
		h.event("exited", fmt.Sprintf("%s generation %d pid %d terminated", g.record.Tool, g.record.Generation, g.record.PluginPID))
	})
}
func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	var gs []*generation
	for _, rt := range h.tools {
		if rt.active != nil {
			gs = append(gs, rt.active)
			rt.active = nil
		}
		gs = append(gs, rt.retiring...)
	}
	h.mu.Unlock()
	for _, g := range gs {
		h.retire(g)
	}
	h.reloadMu.Lock()
	h.reloadMu.Unlock()
}
func (o Output) JSON() string { b, _ := json.Marshal(o); return string(b) }
