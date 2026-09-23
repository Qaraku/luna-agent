package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

// Model-visible tool names. The core registers exactly the tools in Allowlist,
// and neither the browser nor the model can name anything else: a tool name and
// a candidate name only ever come from these tables.
const (
	ToolTextTransform = "luna_text_transform"
	ToolReadFile      = "luna_read_file"
)

// ToolSpec is one allowlisted tool: its model-visible name, the directory under
// plugins/ that holds its candidates, and the candidate versions this core may
// compile and run. Candidate names keep the v1/v2/broken style, so the shape of
// a replacement is identical for every tool.
type ToolSpec struct {
	Tool       string
	Dir        string
	Candidates []string
}

// allows reports whether candidate belongs to this tool's allowlist.
func (s ToolSpec) allows(candidate string) bool {
	for _, allowed := range s.Candidates {
		if allowed == candidate {
			return true
		}
	}
	return false
}

// Allowlist is the complete set of tool plugins the core may run. A candidate
// build path is derived from it, never from a request.
var Allowlist = []ToolSpec{
	{Tool: ToolTextTransform, Dir: "text_transform", Candidates: []string{"v1", "v2", "broken"}},
	{Tool: ToolReadFile, Dir: "read_file", Candidates: []string{"v1", "v2", "broken"}},
}

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
	Candidate  string `json:"candidate"`
	Status     string `json:"status"`
	Inflight   int    `json:"inflight"`
}
type Event struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}
type State struct {
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
	BuildTimeout time.Duration
	StartTimeout time.Duration
	RPCTimeout   time.Duration
	// ReadRoot bounds luna_read_file. It defaults to the repository root and is
	// resolved once, at construction, so the containment check compares real
	// directories rather than symbolic links.
	ReadRoot string
	// ReadLimit is the single-read size cap in bytes. It defaults to
	// fileread.DefaultLimit (256 KiB).
	ReadLimit int
}

// ReadRequest is a file-read request from the core. Path is the raw,
// model-supplied path: the host validates it and the plugin never sees it.
type ReadRequest struct {
	Path string
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

// New starts every allowlisted tool on the default candidate v1. A failure
// leaves nothing running, because a host that could not start its whole tool
// set must not be returned as usable.
func New(ctx context.Context, root string, opts Options) (*Host, error) {
	h := &Host{root: root, opts: opts, next: 1}
	h.withDefaults()
	if err := h.resolveReadRoot(); err != nil {
		return nil, err
	}
	for _, spec := range Allowlist {
		h.tools = append(h.tools, &toolRuntime{spec: spec})
	}
	for _, rt := range h.tools {
		g, err := h.start(ctx, rt, "v1")
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
func (h *Host) start(parent context.Context, rt *toolRuntime, candidate string) (*generation, error) {
	if !rt.spec.allows(candidate) {
		return nil, fmt.Errorf("unknown candidate")
	}
	buildCtx, buildCancel := context.WithTimeout(parent, h.opts.BuildTimeout)
	defer buildCancel()
	runtimeDir := filepath.Join(h.root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(runtimeDir, "plugin-"+rt.spec.Dir+"-"+candidate+"-")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	_ = f.Close()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	dir := "./plugins/" + rt.spec.Dir + "/" + candidate
	cmd := exec.CommandContext(buildCtx, "go", "build", "-o", path, dir)
	cmd.Dir = h.root
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build %s: %w: %.2000s", dir, err, out)
	}
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
	if err != nil || meta.Version != candidate || meta.Protocol != 1 || proc.Process == nil || meta.PID != proc.Process.Pid {
		client.Kill()
		return nil, fmt.Errorf("invalid plugin metadata for %s", dir)
	}
	stopWatch()
	if err := startCtx.Err(); err != nil {
		client.Kill()
		return nil, fmt.Errorf("startup deadline: %w", err)
	}
	ok = true
	return &generation{rt: rt, record: Record{Tool: rt.spec.Tool, Version: meta.Version, PluginPID: meta.PID, Candidate: candidate}, client: client, tool: pt, path: path}, nil
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
	s := State{Plugins: []Record{}, Events: append([]Event{}, h.events...)}
	for _, rt := range h.tools {
		if rt.active != nil {
			s.Plugins = append(s.Plugins, rt.active.record)
		}
		for _, g := range rt.retiring {
			s.Plugins = append(s.Plugins, g.record)
		}
	}
	return s
}

// Invoke calls the text-transform tool.
func (h *Host) Invoke(ctx context.Context, in Input) (Output, error) {
	if len(in.Text) > 16384 || in.DelayMS < 0 || in.DelayMS > 3000 {
		return Output{}, fmt.Errorf("text max 16384 bytes; delay_ms must be 0..3000")
	}
	return h.invoke(ctx, ToolTextTransform, in)
}

// ReadFile calls the file-read tool. The requested path is validated here, on
// the host side; the plugin is handed only the resolved absolute path and the
// cap.
func (h *Host) ReadFile(ctx context.Context, req ReadRequest) (Output, error) {
	if len(req.Path) > 4096 {
		return Output{}, fmt.Errorf("path must not exceed 4096 bytes")
	}
	if req.DelayMS < 0 || req.DelayMS > 3000 {
		return Output{}, fmt.Errorf("delay_ms must be 0..3000")
	}
	absolute, err := fileread.Resolve(h.opts.ReadRoot, req.Path, h.opts.ReadLimit)
	if err != nil {
		return Output{}, err
	}
	return h.invoke(ctx, ToolReadFile, Input{Path: absolute, MaxBytes: h.opts.ReadLimit, DelayMS: req.DelayMS})
}
func (h *Host) invoke(ctx context.Context, tool string, in Input) (Output, error) {
	h.mu.Lock()
	rt := h.runtime(tool)
	if rt == nil {
		h.mu.Unlock()
		return Output{}, fmt.Errorf("unknown tool")
	}
	if h.closed || rt.active == nil {
		h.mu.Unlock()
		return Output{}, fmt.Errorf("no active plugin for %s", tool)
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
		r.err = fmt.Errorf("plugin RPC timeout; owned plugin terminated")
	case <-ctx.Done():
		terminated = true
		g.client.Kill()
		r = <-done
		r.err = fmt.Errorf("plugin RPC canceled; owned plugin terminated: %w", ctx.Err())
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

// allowedCandidate reports whether every allowlisted tool has this candidate,
// so one reload can publish a consistent set.
func (h *Host) allowedCandidate(candidate string) bool {
	for _, rt := range h.tools {
		if !rt.spec.allows(candidate) {
			return false
		}
	}
	return true
}

// Reload builds and validates candidate for every allowlisted tool and then
// publishes them as one generation, or changes nothing. A candidate that fails
// on any tool leaves the previous generation of every tool serving.
func (h *Host) Reload(ctx context.Context, candidate string) error {
	if !h.allowedCandidate(candidate) {
		return fmt.Errorf("unknown candidate")
	}
	if !h.reloadMu.TryLock() {
		return fmt.Errorf("reload already in progress")
	}
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("plugin host closed")
	}
	h.event("build_started", candidate)
	h.mu.Unlock()
	var started []*generation
	for _, rt := range h.tools {
		g, err := h.start(ctx, rt, candidate)
		if err != nil {
			for _, x := range started {
				x.client.Kill()
				_ = os.Remove(x.path)
			}
			h.mu.Lock()
			h.event("reload_failed", candidate+" rejected; active unchanged")
			h.mu.Unlock()
			return err
		}
		started = append(started, g)
	}
	h.mu.Lock()
	if h.closed || ctx.Err() != nil {
		h.mu.Unlock()
		for _, g := range started {
			g.client.Kill()
			_ = os.Remove(g.path)
		}
		return fmt.Errorf("reload expired before publish")
	}
	h.next++
	var draining []*generation
	for i, rt := range h.tools {
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
