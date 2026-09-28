package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/Qaraku/luna-agent/internal/command"
	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/uiplugin"
)

type PluginManager interface {
	State() pluginhost.State
	Reload(context.Context, string) error
}
type Runner interface {
	Run(context.Context, agent.RunRequest) (string, error)
}

// Sessions is the durable session store. The HTTP layer owns the wire shapes;
// the store owns the records.
type Sessions interface {
	Create(title string) (string, error)
	Exists(id string) (bool, error)
	List() ([]store.Summary, error)
	Read(id string) (store.Session, error)
	// AppendConfig records a choice a session made about its own runs, such as
	// which model to use. It appends: a session's file is a record of what was
	// asked for, in order, and the newest statement is the one in effect.
	AppendConfig(id string, record store.ConfigRecord) error
}

type Info struct {
	BoundHost    string
	Model        string
	ProviderHost string
	// Missing names the provider settings that were unset when this process
	// started. A Luna in that state is running and cannot answer a run: the
	// settings page that fills the gap is served by this process.
	//
	// Everything from Model down is what this server was built with. It is the
	// whole answer when no ConfigSource was supplied, and the fallback only
	// when reading one fails: with a source, what a run would use right now is
	// asked of the source, because saving a provider takes effect on the next
	// run rather than at the next start.
	Missing []string
	// Models are the models a run may be sent to, the default first. A runtime
	// configured by environment variables alone has exactly one entry.
	Models []ModelRef
	// ReasoningEffort is the level the process was started with, which is how
	// hard the model was asked to think. It is part of the state the browser may
	// read so that the setting is visible in the product instead of only existing
	// in the environment the server was started from. It is never a secret and it
	// is not changeable while the process runs: the model is built once.
	ReasoningEffort string
	// MaxIterations is how many model turns one run may take. It is reported so
	// the interface can show the budget the run is actually working under
	// instead of leaving a number that ends a run invisible.
	MaxIterations int
	WebDir        string
	// UIPluginsDir is the plugins/ui directory holding runtime UI plugins. It
	// is added by S4a and changes no existing field's meaning. An unset value
	// lists no plugins and serves no file rather than failing a request.
	UIPluginsDir string
}
type LifecycleEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}
type State struct {
	HostPID      int       `json:"host_pid"`
	StartedAt    time.Time `json:"started_at"`
	Model        string    `json:"model"`
	ProviderHost string    `json:"provider_host"`
	// ReasoningEffort is empty when no level was chosen, which is not the same as
	// "medium": nothing was sent, so the provider's own default is what applied.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// MaxIterations and RunTimeoutMS are the two run budgets this server
	// enforces: how many model turns a run may take, and how long it may take
	// before it is stopped and reported as cancelled. They are reported because
	// a bound that ends a run has to be visible in the product.
	MaxIterations   int   `json:"max_iterations,omitempty"`
	RunTimeoutMS    int64 `json:"run_timeout_ms,omitempty"`
	ModelConfigured bool  `json:"model_configured"`
	ModelConnected  bool  `json:"model_connected"`
	// ProviderMissing names the provider settings a run started now would still
	// lack. It is empty for a configured Luna, and a run started while it is not
	// empty fails with a sentence naming them rather than with a provider error.
	ProviderMissing []string `json:"provider_missing,omitempty"`
	// ProviderProblem is the reason the provider file could not be read, when it
	// could not be. The values above are then what this process started with,
	// which is not what the next run would use — and a person who hand-edited the
	// file is owed the reason instead of a stale answer.
	ProviderProblem string              `json:"provider_problem,omitempty"`
	Plugins         []pluginhost.Record `json:"plugins"`
	Busy            bool                `json:"busy"`
	CurrentRunID    string              `json:"current_run_id,omitempty"`
	// CurrentSessionID is the session the active run belongs to. It is added by
	// S2a and changes no existing field's meaning.
	CurrentSessionID string           `json:"current_session_id,omitempty"`
	Events           []LifecycleEvent `json:"events"`
	// Capabilities is what each registered capability declares and whether it is
	// in service. It carries none of the capability's own data: what a capability
	// stores is its own business, and the kernel has no view of it.
	Capabilities []capabilityView `json:"capabilities"`
	Demo         bool             `json:"demo"`
}

// sessionSummary is the frozen list shape of GET /api/sessions.
type sessionSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	RunCount  int       `json:"run_count"`
}

// sessionDetail is the replay shape of GET /api/sessions/{id}. Each record is
// the JSONL line itself, so a client replays exactly what is on disk.
//
// Workspace is the workspace the session works in, resolved from the id its
// newest config record names, or null when it works in none. It is reported
// here so a client does not have to read a config record and join it against the
// workspace list to find out where a session works; the records stay on the wire
// unchanged, so this field adds a reading and changes none.
type sessionDetail struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	RunCount  int            `json:"run_count"`
	Truncated bool           `json:"truncated"`
	Workspace *workspaceView `json:"workspace"`
	Records   []store.Record `json:"records"`
}

// pluginsReady reports whether every allowlisted tool has a published
// generation. A single active plugin is not enough: each tool is its own
// process and can fail on its own.
func pluginsReady(records []pluginhost.Record) bool {
	for _, spec := range pluginhost.Allowlist {
		found := false
		for _, record := range records {
			if record.Tool == spec.Tool && record.Status == "active" {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// DefaultRunTimeout is how long one run may take before it is stopped and
// reported as run.cancelled{reason:"timeout"}.
//
// It bounds a run that is going nowhere — a provider that accepted the request
// and then stopped answering, or a model that keeps calling tools — and it is
// not a work budget: a real investigation that reads and searches a repository
// can legitimately take many minutes, so the default sits far above that and a
// deployment that wants another value sets run_timeout. Stop is what ends a run
// the user no longer wants; this is only the backstop behind it.
const DefaultRunTimeout = 15 * time.Minute

// errRunStopped is the cause a Stop request cancels the active run with. The
// value never reaches a client: the wire reason is the fixed word "user".
var errRunStopped = errors.New("run stopped by user")

// errStreamAbandoned is the cause the run is cancelled with when its SSE
// response can no longer be written — the client disconnected, or a write
// failed. The run is stopped the same way a Stop request stops it: a run whose
// output nobody can read must not keep a process alive.
var errStreamAbandoned = errors.New("run stream is no longer writable")

// streamSinkCapacity is how many run events may be buffered ahead of the SSE
// writer.
//
// Backpressure here is blocking and never dropping: a dropped assistant.delta
// corrupts the answer the user sees, so a full sink slows the run down instead
// of losing an event. The capacity is sized for the streaming contract, where a
// model turn produces one event per chunk — an order of magnitude more events
// than the per-turn events this started with — so the writer can fall behind by
// a whole burst before the run feels it.
const streamSinkCapacity = 512

// Option configures what a Server can reach.
type Option func(*Server)

// WithCapabilities supplies the registry the server asks about capabilities:
// which routes they serve, which state they are in, and how to change it.
func WithCapabilities(reg *plugin.Registry) Option { return func(s *Server) { s.capabilities = reg } }

type Server struct {
	plugins      PluginManager
	runner       Runner
	sessions     Sessions
	capabilities *plugin.Registry
	// capabilityPref 是用户对能力启停的选择，由装配根提供（见 capability.go 的
	// CapabilityPreference）。为 nil 时启停只影响这个进程的现状，不落盘。
	capabilityPref CapabilityPreference
	// writeDirs 是用户定下的"允许写入的目录"，由装配根提供（见 write_dirs.go 的
	// WriteDirPreference）。为 nil 时这份偏好读不到也写不了，接口明确报错，不返回一
	// 张与"什么都不许写"分不开的空表。
	writeDirs WriteDirPreference
	// skills is the Skills capability as the interface uses it: which skills
	// are installed and which of them the user turned off. It is supplied by
	// the composition root, which also owns where that preference is stored.
	skills SkillCatalog
	// provider is the file the settings page edits: the named endpoints, their
	// keys and their models, and which of them a run is sent to. It is an
	// interface because where that file lives, and how it is written, belong to
	// the composition root.
	provider ProviderConfig
	// configSource is what a run started right now would use, when the
	// composition root supplied one: the interface asks it instead of reporting
	// what this process started with.
	configSource ConfigSource
	// workspaces are the sets of directories work can happen in, as the
	// composition root stores them. They are the same store the Workspace
	// capability reads, so what this interface defines is what the next run
	// works in.
	workspaces WorkspaceStore
	// commands is the table the composer's slash commands come from. It is
	// served to the browser rather than duplicated there.
	commands  *command.Table
	info      Info
	started   time.Time
	runMu     sync.Mutex
	busy      bool
	runID     string
	sessionID string
	// cancelRun ends the active run's context with a cause, which is what makes
	// a run stoppable from outside. It is set and cleared inside the same
	// critical section as the run slot itself: a busy flag without a handle
	// would name a run nobody can stop, and a handle without a run would name
	// nothing at all.
	cancelRun context.CancelCauseFunc
	// runTimeout is how long one run may take. It is resolved once, when the
	// server is built, so every run this server admits is bounded the same way
	// and the state it reports is the bound it enforces.
	runTimeout time.Duration
	eventMu    sync.Mutex
	events     []LifecycleEvent
	connected  atomic.Bool
	// fallbackRoot is the directory a run works in when its session names no
	// workspace. The file tools already fall back to it inside the host, so a run
	// that reported no directories at all would leave the capabilities that need
	// one refusing while the model could still read everything under it.
	//
	// A session bound to a workspace that is gone is not this case: that run is
	// refused, because the fallback is a different and possibly wider place to
	// read than the directory the session asked for.
	fallbackRoot string
}

// WithRunTimeout sets how long one run may take before it is stopped and
// reported as cancelled with the timeout reason. A non-positive value keeps the
// default.
func WithRunTimeout(d time.Duration) Option { return func(s *Server) { s.runTimeout = d } }

// WithFallbackRoot sets the directory a run works in when its session names no
// workspace, which has to be the same directory the host falls back to for reads.
func WithFallbackRoot(dir string) Option { return func(s *Server) { s.fallbackRoot = dir } }

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

// RunTimeoutFor resolves the run budget a server built with d enforces: what the caller
// configured, or this package's default. A caller that also owns the HTTP server needs the
// resolved value, because the write deadline has to sit above the budget a run may spend
// streaming — resolving it twice would let the two numbers disagree.
func RunTimeoutFor(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultRunTimeout
	}
	return d
}

func New(p PluginManager, r Runner, sessions Sessions, info Info, opts ...Option) http.Handler {
	s := &Server{plugins: p, runner: r, sessions: sessions, info: info, started: time.Now(), runTimeout: DefaultRunTimeout}
	for _, opt := range opts {
		opt(s)
	}
	s.runTimeout = RunTimeoutFor(s.runTimeout)
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
	busy, id, sessionID := s.busy, s.runID, s.sessionID
	s.runMu.Unlock()
	s.eventMu.Lock()
	events := append([]LifecycleEvent{}, s.events...)
	s.eventMu.Unlock()
	// What a run started now would use, when there is a source to ask. A source
	// that cannot be read is reported as a problem rather than hidden: the values
	// this process started with are not what the next run would use, and saying
	// nothing would leave a person believing a broken file was in effect.
	model, providerHost, missing, problem := s.info.Model, s.info.ProviderHost, s.info.Missing, ""
	effort, maxIterations := s.info.ReasoningEffort, s.info.MaxIterations
	if cfg, live, err := s.providerConfigNow(); live {
		if err != nil {
			problem = err.Error()
		} else {
			model, providerHost, missing = cfg.Model, cfg.ProviderHost, cfg.Missing
			effort = cfg.ReasoningEffort
			maxIterations = agent.MaxIterationsFor(cfg)
		}
	}
	return State{HostPID: os.Getpid(), StartedAt: s.started, Model: model, ProviderHost: providerHost, ReasoningEffort: effort, MaxIterations: maxIterations, RunTimeoutMS: s.runTimeout.Milliseconds(), ModelConfigured: model != "" && providerHost != "" && len(missing) == 0, ModelConnected: s.connected.Load(), Plugins: ps.Plugins, Busy: busy, CurrentRunID: id, CurrentSessionID: sessionID, Events: events, Capabilities: capabilityViews(s.capabilities), ProviderMissing: missing, ProviderProblem: problem, Demo: true}
}
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Host != s.info.BoundHost {
		fail(w, 403, fmt.Errorf("Host must match bound address %s", s.info.BoundHost))
		return
	}
	if s.requiresOrigin(r.Method, r.URL.Path) {
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
		plugins := pluginsReady(st.Plugins)
		send(w, 200, map[string]any{"ready": st.ModelConfigured && plugins, "model_configured": st.ModelConfigured, "plugin_active": plugins})
	case "/api/state":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		send(w, 200, s.state())
	case "/api/commands":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.sendCommands(w)
	case "/api/models":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.sendModels(w, r)
	case "/api/reload":
		if r.Method != http.MethodPost {
			method(w, http.MethodPost)
			return
		}
		s.reload(w, r)
	case "/api/sessions":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.listSessions(w)
	case "/api/ui-plugins":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.listUIPlugins(w)
	case "/api/skills":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.sendSkills(w)
	case providerPath:
		switch r.Method {
		case http.MethodGet:
			s.getProvider(w)
		case http.MethodPut:
			s.setProvider(w, r)
		default:
			method(w, "GET, PUT")
		}
	case providerModelsPath:
		if r.Method != http.MethodPost {
			method(w, http.MethodPost)
			return
		}
		s.probeProvider(w, r)
	case workspacesPath:
		switch r.Method {
		case http.MethodGet:
			s.sendWorkspaces(w)
		case http.MethodPost:
			s.createWorkspace(w, r)
		default:
			method(w, "GET, POST")
		}
	case writeDirsPath:
		switch r.Method {
		case http.MethodGet:
			s.getWriteDirs(w)
		case http.MethodPut:
			s.setWriteDirs(w, r)
		default:
			method(w, "GET, PUT")
		}
	case "/api/runs":
		if r.Method != http.MethodPost {
			method(w, http.MethodPost)
			return
		}
		s.run(w, r)
	case "/", "/app.js", "/style.css", "/theme.js":
		if r.Method != http.MethodGet {
			method(w, http.MethodGet)
			return
		}
		s.static(w, r)
	default:
		if id, ok := cancelRunPath(r.URL.Path); ok {
			if r.Method != http.MethodPost {
				method(w, http.MethodPost)
				return
			}
			s.cancel(w, id)
			return
		}
		if id, action, ok := pluginStatePath(r.URL.Path); ok {
			if r.Method != http.MethodPost {
				method(w, http.MethodPost)
				return
			}
			s.setPluginState(w, id, action)
			return
		}
		if name, action, ok := skillStatePath(r.URL.Path); ok {
			if r.Method != http.MethodPost {
				method(w, http.MethodPost)
				return
			}
			s.setSkillState(w, name, action)
			return
		}
		if id, ok := sessionModelPath(r.URL.Path); ok {
			if r.Method != http.MethodPost {
				method(w, http.MethodPost)
				return
			}
			s.setSessionModel(w, r, id)
			return
		}
		if id, ok := sessionWorkspacePath(r.URL.Path); ok {
			if r.Method != http.MethodPost {
				method(w, http.MethodPost)
				return
			}
			s.setSessionWorkspace(w, r, id)
			return
		}
		if id, ok := sessionPathID(r.URL.Path); ok {
			if r.Method != http.MethodGet {
				method(w, http.MethodGet)
				return
			}
			s.readSession(w, id)
			return
		}
		if name, file, ok := uiPluginPathID(r.URL.Path); ok {
			if r.Method != http.MethodGet {
				method(w, http.MethodGet)
				return
			}
			s.serveUIPluginFile(w, name, file)
			return
		}
		route, allowed, known := s.capabilityRoute(r.Method, r.URL.Path)
		switch {
		case route != nil:
			route.ServeHTTP(w, r)
			return
		case known:
			method(w, allowed)
			return
		}
		http.NotFound(w, r)
	}
}

// sessionPathID extracts {id} from /api/sessions/{id}. The remainder is handed
// on unread: the store's id validation is what keeps a separator, a dot or an
// empty string from ever reaching the filesystem as a path.
func sessionPathID(path string) (string, bool) {
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" {
		return "", false
	}
	return id, true
}

// sessionStatus maps a store failure onto the HTTP status. An unknown session is
// a clear 4xx and never a silently created one.
func sessionStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrInvalidID):
		return 400
	case errors.Is(err, store.ErrNotFound):
		return 404
	default:
		return 500
	}
}

func (s *Server) listSessions(w http.ResponseWriter) {
	summaries, err := s.sessions.List()
	if err != nil {
		fail(w, 500, err)
		return
	}
	list := make([]sessionSummary, 0, len(summaries))
	for _, summary := range summaries {
		list = append(list, sessionSummary{ID: summary.ID, Title: summary.Title, UpdatedAt: summary.UpdatedAt, RunCount: summary.RunCount})
	}
	send(w, 200, map[string]any{"sessions": list})
}

func (s *Server) readSession(w http.ResponseWriter, id string) {
	session, err := s.sessions.Read(id)
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	records := session.Records
	if records == nil {
		records = []store.Record{}
	}
	send(w, 200, sessionDetail{ID: session.ID, Title: session.Title, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt, RunCount: session.RunCount, Truncated: session.Truncated, Workspace: s.sessionWorkspace(session), Records: records})
}

// uiPluginRef is the frozen list shape of GET /api/ui-plugins.
type uiPluginRef struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Entry       string `json:"entry"`
}

// uiPluginSkip names a directory the listing did not return as a plugin, and
// why. The reason is a fixed phrase from uiplugin: it names no host path.
type uiPluginSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// listUIPlugins answers with every discoverable runtime UI plugin in name order
// and with the directories that were skipped. A skipped directory is reported,
// never fatal: one broken plugin cannot turn this into a 500.
func (s *Server) listUIPlugins(w http.ResponseWriter) {
	manifests, skipped, err := uiplugin.List(s.info.UIPluginsDir)
	if err != nil {
		fail(w, 500, err)
		return
	}
	plugins := make([]uiPluginRef, 0, len(manifests))
	for _, manifest := range manifests {
		plugins = append(plugins, uiPluginRef{Name: manifest.Name, Title: manifest.Title, Description: manifest.Description, Entry: manifest.Entry})
	}
	refused := make([]uiPluginSkip, 0, len(skipped))
	for _, skip := range skipped {
		refused = append(refused, uiPluginSkip{Name: skip.Name, Reason: skip.Reason})
	}
	send(w, 200, map[string]any{"plugins": plugins, "skipped": refused})
}

// serveUIPluginFile serves one file from inside one plugin's directory with the
// Content-Type its extension declares. No other header is added: this API sets
// no CORS header, exactly like the rest of it.
func (s *Server) serveUIPluginFile(w http.ResponseWriter, name, file string) {
	content, contentType, err := uiplugin.File(s.info.UIPluginsDir, name, file)
	if err != nil {
		fail(w, uiPluginStatus(err), err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(200)
	_, _ = io.WriteString(w, content)
}

// uiPluginPathID splits /api/ui-plugins/<name>/<file>. The name is one path
// segment; everything after it is the file, handed on unread so the plugin name
// and the file check remain the only validators.
func uiPluginPathID(path string) (string, string, bool) {
	const prefix = "/api/ui-plugins/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	name, file, found := strings.Cut(strings.TrimPrefix(path, prefix), "/")
	if !found || name == "" || file == "" {
		return "", "", false
	}
	return name, file, true
}

// uiPluginStatus maps a UI plugin refusal onto the HTTP status. A file that is
// not there is a 404; a request for something this host does not serve is a
// 4xx and never a guess.
func uiPluginStatus(err error) int {
	switch {
	case errors.Is(err, uiplugin.ErrNameInvalid),
		errors.Is(err, fileread.ErrPathInvalid),
		errors.Is(err, fileread.ErrPathAbsolute),
		errors.Is(err, fileread.ErrPathEscape),
		errors.Is(err, fileread.ErrPathOutside),
		errors.Is(err, fileread.ErrSymlinkEscape):
		return 400
	case errors.Is(err, uiplugin.ErrFileType), errors.Is(err, fileread.ErrBinary):
		return 415
	case errors.Is(err, fileread.ErrTooLarge):
		return 413
	case errors.Is(err, fileread.ErrNotFound), errors.Is(err, fileread.ErrNotRegular):
		return 404
	default:
		return 500
	}
}

func method(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	fail(w, 405, fmt.Errorf("method must be %s", allow))
}

// candidateSentence renders the allowlist's candidates the way the refusal has always
// read ("v1, v2 or broken"), so the message follows the table instead of keeping a
// second copy of the names.
func candidateSentence(names []string) string {
	switch len(names) {
	case 0:
		return "one of the allowlisted candidates"
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	}
}

func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Candidate string `json:"candidate"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !pluginhost.CandidateAllowed(in.Candidate) {
		fail(w, 400, fmt.Errorf("candidate must be %s", candidateSentence(pluginhost.CandidateNames())))
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

// cancelResponse is the frozen 202 shape of POST /api/runs/{id}/cancel.
type cancelResponse struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
}

// cancelRunPath splits /api/runs/{id}/cancel. The id is handed on unread: the
// run slot is the only thing that knows whether a run with that id is active,
// so a malformed or unknown id is answered there rather than guessed at here.
func cancelRunPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/api/runs/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/cancel")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// cancel stops the active run. The request carries no body: stopping a run is a
// property of the run slot, and a caller has nothing to say about it beyond
// which run it means.
//
// A stop that names a run which is not the active one is a 404 and never a
// guess. Stopping twice is not an error: the context of a cancelling run is
// already ended, so a repeated request is answered the same way as the first.
func (s *Server) cancel(w http.ResponseWriter, id string) {
	s.runMu.Lock()
	active := s.busy && s.runID == id
	stop := s.cancelRun
	s.runMu.Unlock()
	if !active || stop == nil {
		fail(w, 404, fmt.Errorf("no active run %q to cancel", id))
		return
	}
	stop(errRunStopped)
	send(w, 202, cancelResponse{RunID: id, State: "cancelling"})
}
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message   string `json:"message"`
		SessionID string `json:"session_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Message) == "" || len(in.Message) > 16384 {
		fail(w, 400, fmt.Errorf("message is required and must not exceed 16384 bytes"))
		return
	}
	// A supplied session must exist, and a session's own choices travel with it:
	// the model its runs use, and the directories they may read. Both are read
	// before admission, so an unknown id costs the caller a 4xx and not the
	// single-run slot.
	runModel := ""
	var runRoots []string
	if in.SessionID != "" {
		session, err := s.sessions.Read(in.SessionID)
		if err != nil {
			fail(w, sessionStatus(err), err)
			return
		}
		// A session that chose a model keeps that choice. An empty one means the
		// configuration's default, which the runner resolves.
		if session.Config != nil {
			runModel = session.Config.Model
		}
		// A session bound to a workspace reads inside that workspace's
		// directories. A binding that no longer names a workspace fails the run
		// instead of falling back: the fallback is the configured root, which is
		// a different and possibly wider place to read, and quietly handing the
		// model more than the session asked for is worse than saying the binding
		// is broken. The user can unbind it (or bind another) and run again.
		if session.Config != nil && session.Config.Workspace != "" {
			found, ok := s.workspaceDirs(session.Config.Workspace)
			if !ok {
				fail(w, 409, fmt.Errorf("this session works in workspace %q, which no longer exists; bind another workspace or unbind this one", session.Config.Workspace))
				return
			}
			runRoots = found
		}
	}
	// A session that names no workspace works in the configured root: that is what
	// the file tools fall back to, so the directories the run reports have to be
	// the same ones. Reporting none left luna_run and luna_write_file refusing in
	// a session where the model could still read every file in that root — a run
	// that can read a directory but cannot work in it.
	if len(runRoots) == 0 && s.fallbackRoot != "" {
		runRoots = []string{s.fallbackRoot}
	}
	// The run context carries both ends of the run: a deadline, and a cancel
	// handle the Stop endpoint uses. They are two contexts because they mean two
	// different things — an expired deadline and a caller who changed their mind
	// — and the first cancellation is the one that decides the cause the runner
	// reports.
	runCtx, cancelTimeout := context.WithTimeout(r.Context(), s.runTimeout)
	defer cancelTimeout()
	runCtx, cancelRun := context.WithCancelCause(runCtx)
	// Releasing the run context is this handler's job on every path, including
	// the admission rejection below that never publishes the handle. It runs
	// after the runner has returned, so it never stops a run that is still
	// reporting how it ended.
	defer cancelRun(errStreamAbandoned)
	s.runMu.Lock()
	if s.busy {
		s.runMu.Unlock()
		fail(w, 409, fmt.Errorf("another run is active"))
		return
	}
	id := newRunID()
	s.busy = true
	s.runID = id
	s.cancelRun = cancelRun
	s.runMu.Unlock()
	// 终止事件发出前释放准入；defer 仍覆盖前置错误，但不能清除后来已开始的运行。
	releaseRun := func() {
		s.runMu.Lock()
		if s.runID == id {
			s.busy = false
			s.sessionID = ""
			s.runID = ""
			s.cancelRun = nil
		}
		s.runMu.Unlock()
	}
	defer releaseRun()
	// A run without a session id starts a session; its id reaches the client on
	// the existing run.started event, and no terminal event is added or changed.
	sessionID := in.SessionID
	if sessionID == "" {
		created, err := s.sessions.Create(in.Message)
		if err != nil {
			fail(w, sessionStatus(err), fmt.Errorf("create session: %w", err))
			return
		}
		sessionID = created
	}
	s.runMu.Lock()
	s.sessionID = sessionID
	s.runMu.Unlock()
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
	sink := &streamSink{ctx: runCtx, events: make(chan agent.Event, streamSinkCapacity)}
	result := make(chan runResult, 1)
	go func() {
		answer, err := s.runner.Run(runCtx, agent.RunRequest{RunID: id, SessionID: sessionID, Message: in.Message, Model: runModel, Roots: runRoots, Sink: sink})
		close(sink.events)
		result <- runResult{answer, err}
	}()
	var terminal agent.Event
	canWrite := true
	events := (<-chan agent.Event)(sink.events)
	resultCh := (<-chan runResult)(result)
	done := runCtx.Done()
	var out runResult
	for events != nil || resultCh != nil {
		select {
		case <-done:
			// The run context ended, so the run is already stopping; only the
			// stream's writability is left to record.
			if r.Context().Err() != nil {
				canWrite = false
			}
			done = nil
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if agent.IsTerminalEvent(ev.Type) {
				// emit 不等于 Runner 已退出。保留首个终止事件，等事件收齐且
				// Runner 返回后再发送，不能让客户端在收尾期间开始下一轮。
				if terminal.Type == "" {
					terminal = ev
				}
				continue
			}
			if canWrite {
				if r.Context().Err() != nil {
					canWrite = false
					cancelRun(errStreamAbandoned)
					done = nil
				} else if err := writeSSE(w, flusher, ev); err != nil {
					canWrite = false
					cancelRun(errStreamAbandoned)
					done = nil
				}
			}
		case out = <-resultCh:
			resultCh = nil
		}
	}
	// The run's own terminal event is the runner's word for how it ended. The
	// synthesis below exists only for a runner that wrote none, and it uses the
	// same classification, so a stopped run is never reported as a broken one.
	outcome := agent.TerminalEvent(runCtx, id, out.answer, out.err)
	if terminal.Type == "" {
		terminal = outcome
	}
	switch outcome.Type {
	case "run.finished":
		s.connected.Store(true)
		s.addEvent("run_finished", "run "+id+" finished")
	case "run.cancelled":
		s.addEvent("run_cancelled", "run "+id+" cancelled")
	default:
		s.addEvent("run_failed", "run "+id+" failed")
	}
	// 此时没有运行中的工具或未完成的 Runner 收尾。客户端看见终止事件时，
	// 状态和准入已经就绪；即使下一轮马上开始，旧 handler 的清理也不会误伤它。
	releaseRun()
	if canWrite && r.Context().Err() != nil {
		canWrite = false
		cancelRun(errStreamAbandoned)
	}
	if canWrite {
		_ = writeSSE(w, flusher, terminal)
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
