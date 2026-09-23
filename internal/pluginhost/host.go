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

	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

type Input = pluginprotocol.Input
type Output struct {
	Result     string `json:"result"`
	Generation uint64 `json:"generation"`
	Version    string `json:"version"`
	PluginPID  int    `json:"plugin_pid"`
}
type Record struct {
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
	Active  *Record  `json:"active"`
	Plugins []Record `json:"plugins"`
	Events  []Event  `json:"events"`
}
type Options struct {
	BuildTimeout time.Duration
	StartTimeout time.Duration
	RPCTimeout   time.Duration
}
type generation struct {
	record Record
	client *plugin.Client
	tool   pluginprotocol.Tool
	path   string
	stop   sync.Once
}
type Host struct {
	mu          sync.Mutex
	reloadMu    sync.Mutex
	next        uint64
	root        string
	opts        Options
	active      *generation
	generations []*generation
	events      []Event
	closed      bool
}

func defaults(o Options) Options {
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = 60 * time.Second
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 5 * time.Second
	}
	if o.RPCTimeout <= 0 {
		o.RPCTimeout = 5 * time.Second
	}
	return o
}
func New(ctx context.Context, root string, opts Options) (*Host, error) {
	h := &Host{root: root, opts: defaults(opts), next: 1}
	g, err := h.start(ctx, "v1")
	if err != nil {
		return nil, err
	}
	g.record.Generation = 1
	g.record.Status = "active"
	h.active = g
	h.generations = []*generation{g}
	h.event("activated", "v1 generation 1 ready")
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
func (h *Host) start(parent context.Context, candidate string) (*generation, error) {
	if candidate != "v1" && candidate != "v2" && candidate != "broken" {
		return nil, fmt.Errorf("unknown candidate")
	}
	buildCtx, buildCancel := context.WithTimeout(parent, h.opts.BuildTimeout)
	defer buildCancel()
	runtimeDir := filepath.Join(h.root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(runtimeDir, "plugin-"+candidate+"-")
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
	cmd := exec.CommandContext(buildCtx, "go", "build", "-o", path, "./plugins/"+candidate)
	cmd.Dir = h.root
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build %s: %w: %.2000s", candidate, err, out)
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
		return nil, fmt.Errorf("handshake %s: %w", candidate, err)
	}
	raw, err := rpcClient.Dispense("tool")
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("dispense %s: %w", candidate, err)
	}
	pt, valid := raw.(pluginprotocol.Tool)
	if !valid {
		client.Kill()
		return nil, fmt.Errorf("invalid tool interface")
	}
	meta, err := pt.Metadata()
	if err != nil || meta.Version != candidate || meta.Protocol != 1 || proc.Process == nil || meta.PID != proc.Process.Pid {
		client.Kill()
		return nil, fmt.Errorf("invalid plugin metadata for %s", candidate)
	}
	stopWatch()
	if err := startCtx.Err(); err != nil {
		client.Kill()
		return nil, fmt.Errorf("startup deadline: %w", err)
	}
	ok = true
	return &generation{record: Record{Version: meta.Version, PluginPID: meta.PID, Candidate: candidate}, client: client, tool: pt, path: path}, nil
}
func (h *Host) event(kind, msg string) {
	h.events = append(h.events, Event{Time: time.Now(), Type: kind, Message: msg})
	if len(h.events) > 100 {
		h.events = h.events[len(h.events)-100:]
	}
}
func (h *Host) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := State{Plugins: []Record{}, Events: append([]Event{}, h.events...)}
	if h.active != nil {
		r := h.active.record
		s.Active = &r
	}
	for _, g := range h.generations {
		s.Plugins = append(s.Plugins, g.record)
	}
	return s
}
func (h *Host) Invoke(ctx context.Context, in Input) (Output, error) {
	if len(in.Text) > 16384 || in.DelayMS < 0 || in.DelayMS > 3000 {
		return Output{}, fmt.Errorf("text max 16384 bytes; delay_ms must be 0..3000")
	}
	h.mu.Lock()
	if h.closed || h.active == nil {
		h.mu.Unlock()
		return Output{}, fmt.Errorf("no active plugin")
	}
	g := h.active
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
		if h.active == g {
			h.active = nil
		}
	}
	drain := g.record.Status != "active" && g.record.Inflight == 0
	h.event("invoke_finished", fmt.Sprintf("generation %d completed (error=%v)", g.record.Generation, r.err != nil))
	h.mu.Unlock()
	if drain {
		h.retire(g)
	}
	return Output{Result: r.v, Generation: g.record.Generation, Version: g.record.Version, PluginPID: g.record.PluginPID}, r.err
}
func (h *Host) Reload(ctx context.Context, candidate string) error {
	if candidate != "v1" && candidate != "v2" && candidate != "broken" {
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
	g, err := h.start(ctx, candidate)
	h.mu.Lock()
	if err != nil {
		h.event("reload_failed", candidate+" rejected; active unchanged")
		h.mu.Unlock()
		return err
	}
	if h.closed || ctx.Err() != nil {
		h.mu.Unlock()
		g.client.Kill()
		_ = os.Remove(g.path)
		return fmt.Errorf("reload expired before publish")
	}
	old := h.active
	h.next++
	g.record.Generation = h.next
	g.record.Status = "active"
	h.active = g
	h.generations = append(h.generations, g)
	drain := false
	if old != nil {
		old.record.Status = "retiring"
		drain = old.record.Inflight == 0
	}
	h.event("activated", fmt.Sprintf("%s generation %d pid %d", candidate, g.record.Generation, g.record.PluginPID))
	h.mu.Unlock()
	if drain {
		h.retire(old)
	}
	return nil
}
func (h *Host) retire(g *generation) {
	g.stop.Do(func() {
		g.client.Kill()
		_ = os.Remove(g.path)
		h.mu.Lock()
		defer h.mu.Unlock()
		for i, x := range h.generations {
			if x == g {
				h.generations = append(h.generations[:i], h.generations[i+1:]...)
				break
			}
		}
		h.event("exited", fmt.Sprintf("generation %d pid %d terminated", g.record.Generation, g.record.PluginPID))
	})
}
func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.active = nil
	gs := append([]*generation{}, h.generations...)
	h.mu.Unlock()
	for _, g := range gs {
		h.retire(g)
	}
	h.reloadMu.Lock()
	h.reloadMu.Unlock()
}
func (o Output) JSON() string { b, _ := json.Marshal(o); return string(b) }
