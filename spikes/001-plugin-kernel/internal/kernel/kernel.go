package kernel

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"io"
	"luna-plugin-demo/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Input = protocol.Input
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
	HostPID   int       `json:"host_pid"`
	StartedAt time.Time `json:"started_at"`
	Active    *Record   `json:"active"`
	Plugins   []Record  `json:"plugins"`
	Events    []Event   `json:"events"`
	Demo      bool      `json:"demo"`
}
type generation struct {
	record Record
	client *plugin.Client
	tool   protocol.Tool
	path   string
	stop   sync.Once
}
type Kernel struct {
	mu          sync.Mutex
	reloadMu    sync.Mutex
	next        uint64
	root        string
	started     time.Time
	active      *generation
	generations []*generation
	events      []Event
	closed      bool
}

func New(ctx context.Context, root string) (*Kernel, error) {
	k := &Kernel{root: root, started: time.Now(), next: 1}
	g, err := k.start(ctx, "v1")
	if err != nil {
		return nil, err
	}
	g.record.Generation = 1
	g.record.Status = "active"
	k.active = g
	k.generations = append(k.generations, g)
	k.event("activated", "v1 generation 1 ready")
	return k, nil
}
func minimalEnv() []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}
func (k *Kernel) start(ctx context.Context, candidate string) (*generation, error) {
	if candidate != "v1" && candidate != "v2" && candidate != "broken" {
		return nil, fmt.Errorf("unknown candidate")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	runtime := filepath.Join(k.root, ".runtime")
	if err := os.MkdirAll(runtime, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(runtime, "plugin-"+candidate+"-")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	ok := false
	defer func() {
		if !ok {
			os.Remove(path)
		}
	}()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, "./plugins/"+candidate)
	cmd.Dir = k.root
	cmd.Env = minimalEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build %s: %w: %.2000s", candidate, err, out)
	}
	proc := exec.Command(path)
	proc.Env = minimalEnv()
	client := plugin.NewClient(&plugin.ClientConfig{HandshakeConfig: protocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &protocol.ToolPlugin{}}, Cmd: proc, SkipHostEnv: true, AllowedProtocols: []plugin.Protocol{plugin.ProtocolNetRPC}, StartTimeout: 5 * time.Second, Logger: hclog.NewNullLogger(), SyncStdout: io.Discard, SyncStderr: io.Discard})
	// One deadline covers connection, Dispense and Metadata, not only handshake.
	startupCtx, startupCancel := context.WithTimeout(ctx, 5*time.Second)
	defer startupCancel()
	stopWatcher := watchStartup(startupCtx, client.Kill)
	defer stopWatcher()
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("handshake %s: %w", candidate, err)
	}
	raw, err := rpc.Dispense("tool")
	if err != nil {
		client.Kill()
		return nil, err
	}
	tool, okType := raw.(protocol.Tool)
	if !okType {
		client.Kill()
		return nil, fmt.Errorf("invalid tool interface")
	}
	meta, err := tool.Metadata()
	if err != nil || meta.Version != candidate || meta.Protocol != 1 || meta.PID != proc.Process.Pid {
		client.Kill()
		return nil, fmt.Errorf("invalid plugin metadata: %+v (%v)", meta, err)
	}
	stopWatcher()
	if err := startupCtx.Err(); err != nil {
		client.Kill()
		return nil, fmt.Errorf("startup deadline: %w", err)
	}
	ok = true
	return &generation{record: Record{Version: meta.Version, PluginPID: meta.PID, Candidate: candidate}, client: client, tool: tool, path: path}, nil
}
func (k *Kernel) event(kind, message string) {
	k.events = append(k.events, Event{Time: time.Now(), Type: kind, Message: message})
	if len(k.events) > 100 {
		k.events = k.events[len(k.events)-100:]
	}
}
func (k *Kernel) State() State {
	k.mu.Lock()
	defer k.mu.Unlock()
	s := State{HostPID: os.Getpid(), StartedAt: k.started, Demo: true, Plugins: []Record{}, Events: append([]Event{}, k.events...)}
	if k.active != nil {
		r := k.active.record
		s.Active = &r
	}
	for _, g := range k.generations {
		s.Plugins = append(s.Plugins, g.record)
	}
	return s
}
func (k *Kernel) Invoke(in Input) (Output, error) {
	if in.DelayMS < 0 || in.DelayMS > 3000 || len(in.Text) > 16384 {
		return Output{}, fmt.Errorf("text max 16384 bytes; delay_ms must be 0..3000")
	}
	k.mu.Lock()
	if k.closed || k.active == nil {
		k.mu.Unlock()
		return Output{}, fmt.Errorf("no active plugin")
	}
	g := k.active
	g.record.Inflight++
	k.mu.Unlock()
	type reply struct {
		result string
		err    error
	}
	done := make(chan reply, 1)
	go func() { result, err := g.tool.Invoke(in); done <- reply{result, err} }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var r reply
	select {
	case r = <-done:
	case <-timer.C:
		k.mu.Lock()
		g.record.Status = "failed"
		if k.active == g {
			k.active = nil
		}
		k.event("rpc_timeout", fmt.Sprintf("generation %d exceeded 5s; terminating owned plugin", g.record.Generation))
		k.mu.Unlock()
		g.client.Kill()
		r = <-done // Do not release the generation until the underlying RPC returns.
		r.err = fmt.Errorf("RPC exceeded 5s; plugin terminated")
	}
	result, err := r.result, r.err
	k.mu.Lock()
	g.record.Inflight--
	drain := g.record.Status != "active" && g.record.Inflight == 0
	k.event("invoke_finished", fmt.Sprintf("generation %d completed (error=%v)", g.record.Generation, err))
	k.mu.Unlock()
	if drain {
		k.retire(g)
	}
	return Output{Result: result, Generation: g.record.Generation, Version: g.record.Version, PluginPID: g.record.PluginPID}, err
}

// Reload is single-flight: concurrent attempts fail fast, never queue builds.
func (k *Kernel) Reload(ctx context.Context, candidate string) error {
	if !k.reloadMu.TryLock() {
		return fmt.Errorf("reload already in progress")
	}
	defer k.reloadMu.Unlock()
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return fmt.Errorf("kernel closed")
	}
	k.event("build_started", candidate)
	k.mu.Unlock()
	g, err := k.start(ctx, candidate)
	k.mu.Lock()
	if err != nil {
		k.event("reload_failed", fmt.Sprintf("%s: %v; active generation unchanged", candidate, err))
		k.mu.Unlock()
		return err
	}
	if k.closed || ctx.Err() != nil {
		k.mu.Unlock()
		g.client.Kill()
		os.Remove(g.path)
		return fmt.Errorf("kernel closed")
	}
	old := k.active
	k.next++
	g.record.Generation = k.next
	g.record.Status = "active"
	k.active = g
	k.generations = append(k.generations, g)
	drain := false
	if old != nil {
		old.record.Status = "retiring"
		drain = old.record.Inflight == 0
		k.event("retiring", fmt.Sprintf("generation %d inflight=%d", old.record.Generation, old.record.Inflight))
	}
	k.event("activated", fmt.Sprintf("%s generation %d pid %d", candidate, g.record.Generation, g.record.PluginPID))
	k.mu.Unlock()
	if drain {
		k.retire(old)
	}
	return nil
}
func (k *Kernel) retire(g *generation) {
	g.stop.Do(func() {
		g.client.Kill()
		os.Remove(g.path)
		k.mu.Lock()
		defer k.mu.Unlock()
		for i, item := range k.generations {
			if item == g {
				k.generations = append(k.generations[:i], k.generations[i+1:]...)
				break
			}
		}
		k.event("exited", fmt.Sprintf("generation %d pid %d terminated", g.record.Generation, g.record.PluginPID))
	})
}
func (k *Kernel) Close() {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return
	}
	k.closed = true
	k.active = nil
	gs := append([]*generation{}, k.generations...)
	k.mu.Unlock()
	for _, g := range gs {
		k.retire(g)
	}
	k.reloadMu.Lock()
	k.reloadMu.Unlock()
}
