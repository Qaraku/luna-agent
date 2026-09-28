package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/command"
	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/httpapi"
	"github.com/Qaraku/luna-agent/internal/layout"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/plugins/memory"
	skillsplugin "github.com/Qaraku/luna-agent/internal/plugins/skills"
	"github.com/Qaraku/luna-agent/internal/plugins/workspace"
	"github.com/Qaraku/luna-agent/internal/provider"
	"github.com/Qaraku/luna-agent/internal/settings"
	"github.com/Qaraku/luna-agent/internal/skills"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/uiplugin"
	workspacedata "github.com/Qaraku/luna-agent/internal/workspace"
)

// rootFromExecutable assumes the conventional layout where the built binary
// lives at <root>/.runtime/luna.
func rootFromExecutable(executable string) string { return filepath.Dir(filepath.Dir(executable)) }

// checkAssets reports why dir cannot serve the runtime. A usable root holds a
// web/index.html file and a plugins directory.
func checkAssets(dir string) error {
	index, err := os.Stat(filepath.Join(dir, "web", "index.html"))
	if err != nil {
		return fmt.Errorf("web/index.html: %w", err)
	}
	if index.IsDir() {
		return errors.New("web/index.html is a directory")
	}
	plugins, err := os.Stat(filepath.Join(dir, "plugins"))
	if err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	if !plugins.IsDir() {
		return errors.New("plugins exists but is not a directory")
	}
	return nil
}

// hasAssets reports whether dir is a usable root.
func hasAssets(dir string) bool { return dir != "" && checkAssets(dir) == nil }

// workingDirOrEmpty returns the process working directory. That directory can be
// removed or renamed out from under the process, which only costs the weakest root
// candidate, so the failure becomes an empty candidate instead of a fatal error.
func workingDirOrEmpty() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// resolveRoot locates the directory holding web/ and plugins/. An explicit -root
// wins and must be usable. Otherwise the executable's assumed grandparent is tried
// first, then the working directory, which is the candidate that makes
// `go run ./cmd/luna` work from a fresh checkout.
func resolveRoot(explicit, executable, workingDir string) (string, error) {
	if explicit != "" {
		if err := checkAssets(explicit); err != nil {
			return "", fmt.Errorf("root %q is not usable: %w", explicit, err)
		}
		return explicit, nil
	}
	var tried []string
	for _, candidate := range []string{rootFromExecutable(executable), workingDir} {
		if candidate == "" || slices.Contains(tried, candidate) {
			continue
		}
		if hasAssets(candidate) {
			return candidate, nil
		}
		tried = append(tried, candidate)
	}
	if len(tried) == 0 {
		return "", errors.New("cannot locate web/ and plugins/; pass -root")
	}
	return "", fmt.Errorf("cannot locate web/ and plugins/; tried %s; pass -root", strings.Join(tried, ", "))
}

// projectRoot resolves the directory a capability may treat as the project the
// agent is working in: the read root the file tool is bounded to. An unset read
// root means the repository root, which is the same default the plugin host
// applies; resolving it here, once, keeps the identity the model is given and
// the boundary the host enforces from drifting apart.
func projectRoot(root, explicitReadRoot string) string {
	if explicitReadRoot != "" {
		return explicitReadRoot
	}
	return root
}

// localData decides where the user's own local data lives: the sessions and the
// capability state root. It reports one sentence per decision, so startup can say
// which location is in use instead of leaving it to be guessed.
//
// The new location is the user's data root — sessions in <data>/sessions, and the
// state root at <data> itself, so the memory capability's file lands at
// <data>/.runtime/memory.jsonl (the namespace is that capability's own claim; the
// kernel only decides the root). Before this version both lived in the checkout:
// <root>/.runtime/sessions, and the checkout root as the state root.
//
// Nothing is moved and nothing is deleted here: a location that already holds data
// wins over an empty one, and when neither holds anything — a fresh checkout, or
// one that was only built — the new location is used.
func localData(dataRoot, repoRoot string) (sessions string, state string, notes []string) {
	newSessions := filepath.Join(dataRoot, "sessions")
	oldSessions := filepath.Join(repoRoot, ".runtime", "sessions")
	sessionsChoice := layout.ChooseDir("sessions", newSessions, oldSessions,
		dirExists(newSessions), dirExists(oldSessions))
	// The state root used to be the checkout root, so a directory alone proves
	// nothing: every checkout that ran once has .runtime/, and one that was only
	// built has .runtime/luna. What tells the two apart is the capability's own
	// file, which is what memoryFileUnder asks for.
	stateChoice := layout.ChooseDir("capability state", dataRoot, repoRoot,
		fileExists(memoryFileUnder(dataRoot)), fileExists(memoryFileUnder(repoRoot)))
	return sessionsChoice.Dir, stateChoice.Dir, []string{sessionsChoice.Reason, stateChoice.Reason}
}

// memoryFileUnder is where the memory capability's file lives when the state root
// is root. The namespace and the file name both belong to that capability, so this
// asks it rather than hardcoding a path that would then be a second source of
// truth for where its data is.
func memoryFileUnder(root string) string {
	dir, err := plugin.StateDirFor(memory.Descriptor(), root)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, memory.StateFileName)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// pinnedLocations is where the sessions and the capability state go when
// LUNA_HOME names one directory for everything.
//
// It does not consult the previous location, and that is the point: LUNA_HOME is
// a direct request about where Luna's files live, and a checkout that has ever
// run would otherwise outvote it — leaving the one variable that promises "all of
// it, here" unable to deliver in exactly the case someone reaches for it.
func pinnedLocations(paths layout.Paths) (sessions string, state string) {
	return filepath.Join(paths.Data, "sessions"), paths.State
}

// explicitOr applies a value given on the command line on top of the resolved
// location, and reports when it did: a log that showed only the resolved path
// would read as if the flag had not been given.
func explicitOr(explicit, resolved, name string) (string, string) {
	if explicit != "" {
		return explicit, name + ": using the directory given on the command line (" + explicit + ")"
	}
	return resolved, ""
}

// workspaceFile resolves where the user's workspaces are stored. They are data
// rather than configuration: a workspace names directories the user works in,
// and losing it would mean defining it again.
func workspaceFile(dataDir string) string { return filepath.Join(dataDir, workspacedata.FileName) }

// workspaceLookup answers which workspace a session works in, by reading the
// session's own newest config record and then the workspace store. The
// association lives in the session file (that is what makes it a property of the
// session) and the workspace itself lives in the workspace file, so this is the
// one place the two are joined.
//
// Every way of not having a workspace answers the same thing — no workspace —
// because the capability's alternative is the fallback identity it has always
// rendered: a session with no config record, a record naming no workspace, and a
// record naming one that no longer exists are all sessions that work in no
// particular workspace.
func workspaceLookup(sessions *store.Store, items *workspacedata.Store) workspace.Lookup {
	return func(sessionID string) (workspace.Target, bool) {
		session, err := sessions.Read(sessionID)
		if err != nil || session.Config == nil || session.Config.Workspace == "" {
			return workspace.Target{}, false
		}
		found, ok := items.Get(session.Config.Workspace)
		if !ok {
			return workspace.Target{}, false
		}
		return workspace.Target{Name: found.Name, Dirs: found.Dirs}, true
	}
}

// defaultRulesName is the file a project keeps its own rules in, and therefore the
// one the Workspace capability contributes when the operator did not name another.
// Pointing -rules-file somewhere else stays possible; the point of the default is
// that a project agent reads its project's rules without being told to.
const defaultRulesName = "AGENTS.md"

// fileExists reports whether a path is a regular file: the project rules file we
// could read, or a capability's own state file when compatibility asks whether
// there is data in a location. A directory is not a file here, and neither is a
// path that cannot be looked up.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// resolveRulesFile decides which file to read the project's rules from. An
// explicit -rules-file always wins; otherwise the project's own AGENTS.md is used
// when it is there, because a project agent that does not read its project's rules
// is a project agent in name only. Neither being present is not an error: it just
// means there are no rules to contribute.
func resolveRulesFile(explicit, root string) string {
	if explicit != "" {
		return explicit
	}
	candidate := filepath.Join(root, defaultRulesName)
	if fileExists(candidate) {
		return candidate
	}
	return ""
}

// loadRules reads the project-rule file the Workspace capability will
// contribute.
//
// Reading belongs to this layer, not to the capability: the capability never
// opens a path, so it needs no filesystem permission — and the Kernel only
// enforces permissions it can, so a permission declared but not enforced would
// be a claim that means nothing. Here there is a path, and the file is read
// with the capability's own text ceiling.
//
// The second return value is a problem statement for the operator when no rules
// can be contributed: a file that cannot be opened, that is empty, or that is
// larger than the ceiling yields no text rather than a truncated rule set. The
// statement names the file, never the absolute path it sits in, and it is for
// the operator only — it is never handed to the capability, so it cannot reach
// the model.
func loadRules(path string, maxText int) (text, problem string) {
	if path == "" {
		return "", ""
	}
	name := filepath.Base(path)
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Sprintf("project rules file %q cannot be opened: %s", name, fileReason(err))
	}
	defer file.Close()
	// One byte past the ceiling, so a file that is too large is recognised as
	// too large instead of being silently cut to fit.
	data, err := io.ReadAll(io.LimitReader(file, int64(maxText)+1))
	if err != nil {
		return "", fmt.Sprintf("project rules file %q cannot be read: %s", name, fileReason(err))
	}
	text = strings.TrimSpace(string(data))
	if text == "" {
		return "", fmt.Sprintf("project rules file %q is empty", name)
	}
	if len(text) > maxText {
		return "", fmt.Sprintf("project rules file %q is larger than %d bytes", name, maxText)
	}
	return text, ""
}

// fileReason reduces an *fs.PathError to its underlying reason. os errors echo
// the path they were given, and a message about a rules file must not carry the
// host's absolute layout.
func fileReason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// providerRuntime is the provider file as the rest of the program uses it: one
// object, because the runner and the interface have to agree about which provider
// a run is sent to.
//
// Read and Write are the settings page's view of the file. Current is what a run
// started right now would use, re-read from disk every time it is asked — that is
// what makes saving a provider take effect on the next run rather than at the next
// start, and it is the same answer the runner builds its client from, so the
// interface can never report a provider the run would not use.
//
// The user's own configuration file is read once, at startup: the settings page
// does not write it, and its budgets belong to this run of the process.
type providerRuntime struct {
	path       string
	getenv     func(string) string
	userConfig *config.File
}

func (p providerRuntime) Read() (provider.File, error) {
	file, _, err := provider.LoadFile(p.path)
	return file, err
}

func (p providerRuntime) Write(file provider.File) error {
	if err := provider.Save(p.path, file); err != nil {
		return err
	}
	log.Printf("luna: provider: saved to %s", p.path)
	return nil
}

// Current resolves the configuration a run started now would use: the active
// provider in the file, plus the run budgets and the reasoning level from the
// user's configuration file and the environment.
func (p providerRuntime) Current() (config.Config, error) {
	file, _, err := provider.LoadFile(p.path)
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(p.getenv, p.userConfig, providerValues(file))
}

// providerValues is the active provider as the configuration layer reads it. A
// file with no provider in use yields no values, and the loader then reports what
// is still unset instead of failing: a Luna that has never been configured is a
// starting state, not a broken installation.
func providerValues(file provider.File) config.ProviderValues {
	name, entry, ok := file.Selection()
	if !ok {
		return config.ProviderValues{}
	}
	return config.ProviderValues{Name: name, BaseURL: entry.BaseURL, APIKey: entry.APIKey, Model: entry.Model, Models: entry.Models}
}

// initialProvider reads the provider file once, at startup, and says what it
// found. It returns a configuration and a sentence, never an error: the caller
// logs the sentence and starts anyway.
//
// Nothing is papered over by that. A file that could not be read leaves the
// configuration empty — the same state as a Luna that has never been configured —
// and the runtime then answers the reason on every request and every run, which is
// what the settings page shows and what a run fails with. What it avoids is the
// alternative: exiting, which would make the one surface that can repair the file
// unreachable, on an installation whose only problem is a file it owns.
func initialProvider(runtime providerRuntime, path string) (config.Config, string) {
	cfg, err := runtime.Current()
	if err != nil {
		return config.Config{}, fmt.Sprintf("provider: %s could not be read: %v — fix it on the settings page or in that file; a run reports this until then", path, err)
	}
	if len(cfg.Missing) > 0 {
		return cfg, fmt.Sprintf("provider: not configured yet — %s is unset; fill it in on the settings page", strings.Join(cfg.Missing, ", "))
	}
	file, found, readErr := provider.LoadFile(path)
	if readErr == nil && found {
		if name, entry, ok := file.Selection(); ok {
			return cfg, fmt.Sprintf("provider: %s calls %s for model %s; another provider can be chosen on the settings page and the next run uses it", name, entry.BaseURL, entry.Model)
		}
	}
	return cfg, "provider: using " + path
}

// configFileFor decides which configuration file to read: an explicit
// -config-file always wins, otherwise the file lives in the user's own
// configuration directory, which is the one place a user can edit without
// knowing anything about where Luna is installed.
func configFileFor(explicit, configDir string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(configDir, config.FileName)
}

// providerFileFor is where the provider file lives: beside the user's other
// configuration, in the directory they own. There is no flag for it, because it
// is not a launch-time choice — it is the installation's provider, and the
// settings page writes it where the next start will read it.
func providerFileFor(configDir string) string {
	return filepath.Join(configDir, provider.FileName)
}

// userConfig reads the user's configuration file and returns it, or nil when
// there is none. A missing file is not an error: the environment alone is a
// complete configuration, and it is the only one Luna had for a long time.
//
// Which file was read is reported, because a user who edited the file and sees
// no effect needs to know whether Luna looked at it at all.
func userConfig(path string) (*config.File, error) {
	file, found, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	log.Printf("luna: reading user configuration from %s", path)
	return &file, nil
}

// settingsFileFor is where Luna's own preference file lives: beside the user's
// configuration file, because that is the directory a user can find without
// knowing anything about where Luna is installed. It is not a flag: the user
// edits config.yaml, and Luna writes this one, so which file it is is not a
// choice either side needs to make.
func settingsFileFor(configDir string) string {
	return filepath.Join(configDir, settings.FileName)
}

// loadUserSettings reads the preference file Luna owns. A missing file is not an
// error: a user who has never turned a skill off has no settings at all, and
// that is the same state as an empty one.
//
// A file that is there but unreadable or unrecognised fails the start, carrying
// the reason. Starting with every skill on would leave the user with a setting
// that does not apply and no one to tell them why.
//
// Which file was read is reported for the same reason the configuration file's
// path is: someone who edited it and sees no effect needs to know whether it was
// read at all.
func loadUserSettings(path string) (settings.Settings, error) {
	file, found, err := settings.Load(path)
	if err != nil {
		return settings.Settings{}, err
	}
	if !found {
		return settings.Settings{}, nil
	}
	log.Printf("luna: reading user settings from %s", path)
	return file, nil
}

// stateRoot resolves where capabilities keep their state: an explicit directory
// wins, otherwise the resolved location — see localData for how that is decided.
// A capability then claims a namespace inside it, and the kernel resolves that
// claim to a directory — never to a file, because which file a capability keeps
// is its own business.

// writeDeadlineFor places the HTTP write deadline above the run budget.
//
// The budget, not the connection, has to be what ends a run. When the deadline is the
// shorter of the two, a run that streams past it is cut at the socket: the browser gets
// no terminal event, and it cannot tell a cut stream from a hang — and the run itself is
// cancelled by the dead connection, so the budget never gets its say. The margin covers
// the last flush and the terminal event written once the budget does fire.
func writeDeadlineFor(runTimeout time.Duration) time.Duration {
	return runTimeout + 30*time.Second
}

// uiPluginsDir resolves the runtime UI plugin directory. It always lives under
// the resolved root and has no flag: a browser is only ever served plugins from
// the checkout that is running, never from a path a request asked for.
func uiPluginsDir(root string) string {
	return filepath.Join(root, "plugins", uiplugin.Dir)
}

// repeatedPath is a flag that may be given more than once: each value is one more
// directory to look for skills in, so a user can keep several sets apart without
// a separator convention for a path that may itself contain one.
type repeatedPath []string

func (p *repeatedPath) String() string { return strings.Join(*p, ",") }

func (p *repeatedPath) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("a skills directory cannot be empty")
	}
	*p = append(*p, trimmed)
	return nil
}

// skillRoots is every directory discovery reads, in priority order: the
// user-level directory under the data root first, then whatever the command line
// added. The flag adds more of the user's own directories — it is user scope, not
// a project scope, because this version has no source that could establish one.
// The working directory and the installation are deliberately not roots: which
// skills exist must not depend on where Luna was started from.
func skillRoots(paths layout.Paths, extra repeatedPath) []skills.Root {
	roots := []skills.Root{{Path: filepath.Join(paths.Data, "skills"), Scope: skills.ScopeUser}}
	for _, dir := range extra {
		roots = append(roots, skills.Root{Path: dir, Scope: skills.ScopeUser})
	}
	return roots
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "literal loopback listen address")
	rootFlag := flag.String("root", "", "repository root holding web/ and plugins/ (default: auto-detect)")
	readRoot := flag.String("read-root", "", "directory the file tools (luna_read_file, luna_list_dir, luna_search_files, luna_find_files) are bounded to (default: the resolved root)")
	readLimit := flag.Int("read-limit", 0, "single-read cap in bytes for luna_read_file (default: 262144)")
	sessionsFlag := flag.String("sessions-dir", "", "directory holding the append-only session files (default: chosen at startup — <data-dir>/sessions, or the previous <root>/.runtime/sessions/ while the sessions are still there; startup logs which one is in use)")
	stateFlag := flag.String("state-dir", "", "root directory holding capability state (default: chosen at startup — <data-dir>, or the previous <root> while that is where the capability state already is; startup logs which one is in use)")
	rulesFlag := flag.String("rules-file", "", "file holding the project rules the workspace capability contributes (default: none)")
	configFlag := flag.String("config-file", "", "user configuration file to read (default: <config-dir>/config.yaml)")
	skillDirs := repeatedPath{}
	flag.Var(&skillDirs, "skills-dir", "extra directory to discover skills in (may be repeated; the user-level <data-dir>/skills is always read)")
	flag.Parse()
	// Where the user's own files live is a different question from where this
	// copy of Luna is installed. The first follows the XDG directories, the
	// second is found next to the executable; resolving them together is what
	// made "the project" and "the installation" the same thing.
	// pinnedHome carries a LUNA_HOME value down to the location decision: with it
	// set, the previous location has no vote (see pinnedLocations).
	var pinnedHome string
	home, err := os.UserHomeDir()
	if err != nil && strings.TrimSpace(os.Getenv(layout.HomeEnv)) == "" {
		// With LUNA_HOME there is nothing for the home directory to decide, so
		// an environment that cannot report one is not a reason to stop.
		return fmt.Errorf("locate the home directory: %w", err)
	}
	paths, err := layout.Resolve(os.Getenv, home)
	if err != nil {
		return err
	}
	if value := strings.TrimSpace(os.Getenv(layout.HomeEnv)); value != "" {
		// One directory holding everything is a deliberate choice, and the
		// files it holds are otherwise unfindable: configuration, sessions,
		// capability state and cache all live under it and nowhere else.
		pinnedHome = value
		log.Printf("luna: home: %s=%s holds the configuration, the sessions, the capability state and the cache", layout.HomeEnv, value)
	}
	configFile, err := userConfig(configFileFor(*configFlag, paths.Config))
	if err != nil {
		return err
	}
	// Settings are the other half of the user's configuration, and a different
	// file for a different reason: config.yaml is theirs to edit, settings.yaml
	// is Luna's to write. Turning a skill off is a choice, not a state change of
	// the capability, so it is stored where the user's other choices are.
	settingsPath := settingsFileFor(paths.Config)
	userSettings, err := loadUserSettings(settingsPath)
	if err != nil {
		return err
	}
	// The provider file is Luna's own: the settings page writes it, and this is
	// where the runner and the interface read it back. Every way this can go
	// wrong — no file yet, a file an earlier version wrote, a file that cannot be
	// parsed — is a starting state rather than a reason to exit: the settings page
	// that repairs the last of them is served by this process, and a Luna that
	// will not start is a Luna nobody can fix from the interface.
	providerPath := providerFileFor(paths.Config)
	// runtime is the same object the runner and the settings page are handed:
	// one place reads the provider file, so the answer a run uses and the answer
	// the interface shows cannot drift apart.
	runtime := providerRuntime{path: providerPath, getenv: os.Getenv, userConfig: configFile}
	cfg, providerNote := initialProvider(runtime, providerPath)
	log.Printf("luna: %s", providerNote)
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	root, err := resolveRoot(*rootFlag, executable, workingDirOrEmpty())
	if err != nil {
		return err
	}
	var sessionsDefault, stateDefault string
	var dataNotes []string
	if pinnedHome != "" {
		sessionsDefault, stateDefault = pinnedLocations(paths)
	} else {
		sessionsDefault, stateDefault, dataNotes = localData(paths.Data, root)
	}
	sessionsPath, sessionsNote := explicitOr(*sessionsFlag, sessionsDefault, "sessions")
	statePath, stateNote := explicitOr(*stateFlag, stateDefault, "capability state")
	// Which location is in use is a property of the installation, not something
	// the user should have to infer from where files appear.
	for _, line := range append(dataNotes, sessionsNote, stateNote) {
		if line != "" {
			log.Printf("luna: %s", line)
		}
	}
	sessions, err := store.Open(sessionsPath)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	// Capabilities are the product surface; the kernel owns what runs them. The
	// Memory capability is the first one: a built-in plugin that claims a state
	// namespace, which the kernel resolves into a directory under whichever state
	// root is in use — the kernel never learns which file it keeps inside.
	registry := plugin.NewRegistry(plugin.PermissionStateWrite)
	memoryDir, err := plugin.StateDirFor(memory.Descriptor(), statePath)
	if err != nil {
		return fmt.Errorf("resolve memory state directory: %w", err)
	}
	facts, err := memory.New(memoryDir)
	if err != nil {
		return fmt.Errorf("open memory store: %w", err)
	}
	if err := registry.Register(facts); err != nil {
		return fmt.Errorf("register memory capability: %w", err)
	}
	if err := registry.Enable(memory.PluginID); err != nil {
		return fmt.Errorf("enable memory capability: %w", err)
	}
	// The Workspace capability names the project this session is working in and
	// states that project's rules. It is built from the same directory the file
	// tool is bounded to and keeps no state of its own: identity comes from the
	// root, so it claims no namespace and asks for no permission. The rules are
	// read here and handed in as text — the capability opens no file, so it
	// needs no permission the Kernel cannot yet enforce.
	rulesFile := resolveRulesFile(*rulesFlag, projectRoot(root, *readRoot))
	rules, rulesProblem := loadRules(rulesFile, workspace.MaxRulesTextBytes)
	if rulesProblem != "" {
		log.Printf("luna: %s; no project rules will be contributed", rulesProblem)
	}
	// Workspaces are the sets of directories a session can work in. They are
	// read from the data directory, which is where a user's own content lives:
	// losing this file would mean defining the workspaces again. A machine where
	// nobody has defined one yet has no file, and that is not an error.
	workspaceStore, err := workspacedata.Open(workspaceFile(paths.Data))
	if err != nil {
		return fmt.Errorf("open workspace store: %w", err)
	}
	project, err := workspace.New(workspace.Options{
		Root:   projectRoot(root, *readRoot),
		Rules:  rules,
		Lookup: workspaceLookup(sessions, workspaceStore),
		// A rule set that could not be contributed is the operator's business,
		// the same way a fallback rules file that could not be read is: the
		// model sees the rules or nothing, never half an explanation.
		Report: func(problem string) { log.Printf("luna: %s", problem) },
	})
	if err != nil {
		return fmt.Errorf("open workspace capability: %w", err)
	}
	if err := registry.Register(project); err != nil {
		return fmt.Errorf("register workspace capability: %w", err)
	}
	if err := registry.Enable(workspace.PluginID); err != nil {
		return fmt.Errorf("enable workspace capability: %w", err)
	}
	// Skills are files the user owns and Luna only reads. Discovery happens once,
	// here, and what it could not accept is reported rather than hidden: a skill
	// that was rejected or shadowed is exactly the thing its author needs to hear
	// about. The capability owns the manifest and the tool that reads one skill.
	discovered, skillProblems := skills.Discover(skillRoots(paths, skillDirs))
	for _, problem := range skillProblems {
		log.Printf("luna: skill %s", problem)
	}
	skillSet := skillsplugin.New(discovered, userSettings.DisabledSkills()...)
	if err := registry.Register(skillSet); err != nil {
		return fmt.Errorf("register skills capability: %w", err)
	}
	if err := registry.Enable(skillsplugin.PluginID); err != nil {
		return fmt.Errorf("enable skills capability: %w", err)
	}
	listener, err := httpapi.Listen(*addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	plugins, err := pluginhost.New(ctx, root, pluginhost.Options{ReadRoot: projectRoot(root, *readRoot), ReadLimit: *readLimit})
	if err != nil {
		return fmt.Errorf("start plugin host: %w", err)
	}
	defer plugins.Close()
	// The store is both sides of the conversation: history is read from it and
	// the transcript of every run is appended to it. Capabilities are assembled
	// from the registry, which is what makes them capabilities rather than core.
	//
	// The runner is built around the same runtime the interface reads: it asks for
	// the provider at the start of every run, so a provider saved while this
	// process runs is used by the next run without a restart. A Luna that has
	// never been configured is a normal Luna here — its run reports what is
	// missing, and the settings page that fills it in is served by this process.
	runner, err := agent.NewProviderRunner(ctx, runtime, plugins, plugins, agent.WithHistory(sessions), agent.WithTranscript(sessions), agent.WithCapabilities(registry))
	if err != nil {
		return fmt.Errorf("construct Eino agent: %w", err)
	}
	// The command table is the kernel's own list. /model is not declared here:
	// which models exist is a property of the provider file, so the server
	// derives that command from the same runtime on every request.
	commands, err := command.New(command.Builtins()...)
	if err != nil {
		return fmt.Errorf("build the command table: %w", err)
	}
	bound := listener.Addr().String()
	// The values gathered here are what this process started with. They are the
	// whole answer only when the runtime cannot be read; otherwise the interface
	// asks the runtime, because what is stored is what the next run uses.
	models := make([]httpapi.ModelRef, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		models = append(models, httpapi.ModelRef{Name: model.Name, Provider: model.Provider})
	}
	// 运行预算与 HTTP 写入截止时间必须互相说得通：一次运行有权用到它自己的预算为止，
	// 所以写入截止时间要**高于**预算，而不是替预算结束运行（见 writeDeadlineFor）。
	runTimeout := httpapi.RunTimeoutFor(cfg.RunTimeout)
	handler := httpapi.New(plugins, runner, sessions, httpapi.Info{BoundHost: bound, Model: cfg.Model, ProviderHost: cfg.ProviderHost, Missing: cfg.Missing, Models: models, ReasoningEffort: cfg.ReasoningEffort, MaxIterations: agent.MaxIterationsFor(cfg), WebDir: filepath.Join(root, "web"), UIPluginsDir: uiPluginsDir(root)}, httpapi.WithCapabilities(registry), httpapi.WithCommands(commands), httpapi.WithSkills(newSkillCatalog(skillSet, settingsPath, userSettings)), httpapi.WithWorkspaces(workspaceStore), httpapi.WithProvider(runtime), httpapi.WithConfigSource(runtime), httpapi.WithRunTimeout(runTimeout))
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 70 * time.Second, WriteTimeout: writeDeadlineFor(runTimeout), IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		done <- err
	}()
	fmt.Printf("LISTEN_URL=http://%s\n", bound)
	fmt.Printf("ROOT=%s\n", root)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-done
	}
}

func main() {
	if err := run(); err != nil {
		log.Printf("luna: %v", err)
		os.Exit(1)
	}
}
