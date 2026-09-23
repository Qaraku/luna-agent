package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

type PluginManager interface {
	State() pluginhost.State
	Reload(context.Context, string) error
}
type Runner interface {
	Run(context.Context, string, string, agent.Sink) (string, error)
}
type Info struct {
	BoundHost    string
	Model        string
	ProviderHost string
	WebDir       string
}
type LifecycleEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}
type State struct {
	HostPID         int                 `json:"host_pid"`
	StartedAt       time.Time           `json:"started_at"`
	Model           string              `json:"model"`
	ProviderHost    string              `json:"provider_host"`
	ModelConfigured bool                `json:"model_configured"`
	ModelConnected  bool                `json:"model_connected"`
	Active          *pluginhost.Record  `json:"active"`
	Plugins         []pluginhost.Record `json:"plugins"`
	Busy            bool                `json:"busy"`
	CurrentRunID    string              `json:"current_run_id,omitempty"`
	Events          []LifecycleEvent    `json:"events"`
	Demo            bool                `json:"demo"`
}

var runTimeout = 60 * time.Second

type Server struct {
	plugins   PluginManager
	runner    Runner
	info      Info
	started   time.Time
	runMu     sync.Mutex
	busy      bool
	runID     string
	eventMu   sync.Mutex
	events    []LifecycleEvent
	connected atomic.Bool
}

func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("bind must be a literal loopback IP")
	}
	return net.Listen("tcp", addr)
}
func New(p PluginManager, r Runner, info Info) http.Handler {
	s := &Server{plugins: p, runner: r, info: info, started: time.Now()}
	return http.HandlerFunc(s.serveHTTP)
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, err error) {
	send(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if err != nil {
		fail(w, 413, fmt.Errorf("request body exceeds 32768 bytes"))
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, err)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, fmt.Errorf("expected exactly one JSON object"))
		return false
	}
	return true
}
func (s *Server) addEvent(kind, msg string) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	s.events = append(s.events, LifecycleEvent{Time: time.Now(), Type: kind, Message: msg})
	if len(s.events) > 100 {
		s.events = s.events[len(s.events)-100:]
	}
}
func (s *Server) state() State {
	ps := s.plugins.State()
	s.runMu.Lock()
	busy, id := s.busy, s.runID
	s.runMu.Unlock()
	s.eventMu.Lock()
	events := append([]LifecycleEvent{}, s.events...)
	s.eventMu.Unlock()
	return State{HostPID: os.Getpid(), StartedAt: s.started, Model: s.info.Model, ProviderHost: s.info.ProviderHost, ModelConfigured: s.info.Model != "" && s.info.ProviderHost != "", ModelConnected: s.connected.Load(), Active: ps.Active, Plugins: ps.Plugins, Busy: busy, CurrentRunID: id, Events: events, Demo: true}
}
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Host != s.info.BoundHost {
		fail(w, 403, fmt.Errorf("Host must match bound address %s", s.info.BoundHost))
		return
	}
	mutation := r.URL.Path == "/api/reload" || r.URL.Path == "/api/runs"
	if mutation {
		origins, ok := r.Header["Origin"]
		if !ok || len(origins) != 1 || origins[0] != "http://"+s.info.BoundHost {
			fail(w, 403, fmt.Errorf("missing, null or foreign Origin rejected"))
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, 403, fmt.Errorf("cross-site request rejected"))
		return
	}
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		st := s.state()
		ready := st.ModelConfigured && st.Active != nil && st.Active.Status == "active"
		send(w, 200, map[string]any{"ready": ready, "model_configured": st.ModelConfigured, "plugin_active": st.Active != nil})
	case "/api/state":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		send(w, 200, s.state())
	case "/api/reload":
		if r.Method != http.MethodPost {
			method(w, http.MethodPost)
			return
		}
		s.reload(w, r)
	case "/api/runs":
		if r.Method != http.MethodPost {
			method(w, http.MethodPost)
			return
		}
		s.run(w, r)
	case "/", "/app.js", "/style.css":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.static(w, r)
	default:
		http.NotFound(w, r)
	}
}
func method(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	fail(w, 405, fmt.Errorf("method must be %s", allow))
}
func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Candidate string `json:"candidate"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Candidate != "v1" && in.Candidate != "v2" && in.Candidate != "broken" {
		fail(w, 400, fmt.Errorf("candidate must be v1, v2 or broken"))
		return
	}
	if err := s.plugins.Reload(r.Context(), in.Candidate); err != nil {
		s.addEvent("reload_failed", in.Candidate+" rejected; active unchanged")
		fail(w, 409, err)
		return
	}
	s.addEvent("reloaded", in.Candidate+" activated")
	send(w, 200, s.state())
}
func newRunID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message string `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Message) == "" || len(in.Message) > 16384 {
		fail(w, 400, fmt.Errorf("message is required and must not exceed 16384 bytes"))
		return
	}
	s.runMu.Lock()
	if s.busy {
		s.runMu.Unlock()
		fail(w, 409, fmt.Errorf("another run is active"))
		return
	}
	id := newRunID()
	s.busy = true
	s.runID = id
	s.runMu.Unlock()
	defer func() { s.runMu.Lock(); s.busy = false; s.runID = ""; s.runMu.Unlock() }()
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	flusher.Flush()
	s.addEvent("run_started", "run "+id+" started")
	runCtx, cancel := context.WithTimeout(r.Context(), runTimeout)
	defer cancel()
	sink := &streamSink{ctx: runCtx, events: make(chan agent.Event, 32)}
	result := make(chan runResult, 1)
	go func() {
		answer, err := s.runner.Run(runCtx, in.Message, id, sink)
		close(sink.events)
		result <- runResult{answer, err}
	}()
	terminal := 0
	canWrite := true
	events := (<-chan agent.Event)(sink.events)
	resultCh := (<-chan runResult)(result)
	done := runCtx.Done()
	var out runResult
	for events != nil || resultCh != nil {
		select {
		case <-done:
			if r.Context().Err() != nil {
				canWrite = false
			}
			cancel()
			done = nil
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			isTerminal := ev.Type == "run.finished" || ev.Type == "run.failed"
			if isTerminal {
				if terminal > 0 {
					continue
				}
				terminal++
			}
			if canWrite {
				if r.Context().Err() != nil {
					canWrite = false
					cancel()
					done = nil
				} else if err := writeSSE(w, flusher, ev); err != nil {
					canWrite = false
					cancel()
					done = nil
				}
			}
		case out = <-resultCh:
			resultCh = nil
		}
	}
	if terminal == 0 && canWrite {
		typ := "run.finished"
		data := any(agent.RunFinished{RunID: id, Answer: out.answer})
		if out.err != nil {
			typ = "run.failed"
			data = agent.RunFailed{RunID: id, Error: out.err.Error()}
		}
		if r.Context().Err() != nil {
			canWrite = false
			cancel()
		} else {
			_ = writeSSE(w, flusher, agent.Event{Type: typ, Data: data})
		}
		terminal = 1
	}
	if out.err == nil {
		s.connected.Store(true)
		s.addEvent("run_finished", "run "+id+" finished")
	} else {
		s.addEvent("run_failed", "run "+id+" failed")
	}
}

type runResult struct {
	answer string
	err    error
}
type streamSink struct {
	ctx    context.Context
	events chan agent.Event
}

func (s *streamSink) Emit(e agent.Event) {
	select {
	case s.events <- e:
	case <-s.ctx.Done():
	}
}
func writeSSE(w io.Writer, f http.Flusher, e agent.Event) error {
	b, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b); err != nil {
		return err
	}
	f.Flush()
	return nil
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	path := filepath.Join(s.info.WebDir, name)
	base, err := filepath.Abs(s.info.WebDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, abs)
}
