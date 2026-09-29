package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/httpapi"
	"github.com/Qaraku/luna-agent/internal/layout"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/filewrite"
	"github.com/Qaraku/luna-agent/internal/plugins/memory"
	"github.com/Qaraku/luna-agent/internal/plugins/terminal"
	"github.com/Qaraku/luna-agent/internal/plugins/web"
	"github.com/Qaraku/luna-agent/internal/plugins/workspace"
	"github.com/Qaraku/luna-agent/internal/provider"
	"github.com/Qaraku/luna-agent/internal/settings"
	"github.com/Qaraku/luna-agent/internal/skills"
)

// No way of getting the provider file wrong may end the process: the settings page
// that repairs it is served by the same process, so exiting would put the only fix
// out of reach. What could not be read is carried into the sentence that is logged,
// and the runtime answers it per request and per run.
func TestStartupProviderReadingNeverFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), provider.FileName)
	runtime := providerRuntime{path: path, getenv: func(string) string { return "" }}

	// Nothing yet: the normal first run.
	cfg, note := initialProvider(runtime, path)
	if strings.Join(cfg.Missing, ",") != "provider" {
		t.Fatalf("missing = %v, want the provider itself", cfg.Missing)
	}
	if !strings.Contains(note, "not configured yet") {
		t.Fatalf("note = %q", note)
	}

	// A file an earlier version wrote: still usable, and the note names the
	// provider it was read as.
	previous := "base_url: https://old.example.test/v1\napi_key: sk-old\nmodel: old-model\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, note = initialProvider(runtime, path)
	if len(cfg.Missing) != 0 || cfg.Model != "old-model" || cfg.BaseURL != "https://old.example.test/v1" {
		t.Fatalf("cfg = %+v, note = %q", cfg, note)
	}
	if !strings.Contains(note, provider.LegacyName) || !strings.Contains(note, "old.example.test") {
		t.Fatalf("note = %q, want it to name what it read", note)
	}

	// A file that cannot be read: one logged sentence, an empty configuration and
	// no exit.
	broken := "active: nobody\nproviders:\n  a:\n    base_url: https://a.example.test/v1\n    model: m\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, note = initialProvider(runtime, path)
	if !strings.Contains(note, "could not be read") || !strings.Contains(note, "nobody") {
		t.Fatalf("note = %q, want the reason in it", note)
	}
	if cfg.Model != "" || len(cfg.Missing) != 0 {
		t.Fatalf("cfg = %+v, want an empty configuration", cfg)
	}
	// The same reason is what a run and the interface get, and the file is left as
	// it was: repairing it is the user's decision, not a silent rewrite.
	if _, err := runtime.Current(); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Fatalf("Current() = %v, want the reason the file could not be used", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != broken {
		t.Fatalf("the file was rewritten: %s", after)
	}
}

func TestRootFromExecutableRuntimeBinary(t *testing.T) {
	got := rootFromExecutable("/tmp/luna-agent/.runtime/luna")
	if got != "/tmp/luna-agent" {
		t.Fatalf("got %q", got)
	}
}

// makeRoot creates a directory shaped like a repository root: it holds the
// plugins directory and a web/index.html asset.
func makeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatalf("create plugins dir: %v", err)
	}
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatalf("create web dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write web/index.html: %v", err)
	}
	return root
}

func TestHasAssetsRequiresBothAssets(t *testing.T) {
	webOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(webOnly, "web"), 0o755); err != nil {
		t.Fatalf("create web dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webOnly, "web", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	pluginsOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginsOnly, "plugins"), 0o755); err != nil {
		t.Fatalf("create plugins dir: %v", err)
	}

	pluginsIsAFile := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginsIsAFile, "web"), 0o755); err != nil {
		t.Fatalf("create web dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsIsAFile, "web", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsIsAFile, "plugins"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write plugins file: %v", err)
	}

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"complete root", makeRoot(t), true},
		{"missing plugins", webOnly, false},
		{"missing web/index.html", pluginsOnly, false},
		{"plugins is a file", pluginsIsAFile, false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		if got := hasAssets(tc.dir); got != tc.want {
			t.Fatalf("%s: hasAssets(%q) = %v, want %v", tc.name, tc.dir, got, tc.want)
		}
	}
}

func TestResolveRootUsesExplicitRoot(t *testing.T) {
	explicit := makeRoot(t)
	cwd := makeRoot(t)
	got, err := resolveRoot(explicit, filepath.Join(cwd, ".runtime", "luna"), cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != explicit {
		t.Fatalf("got %q, want the explicit root %q", got, explicit)
	}
}

// The working directory is the weakest candidate, so its absence must never
// block an explicit root. This is the case that a removed working directory
// (a deleted checkout, an unlinked temporary directory) produces in practice.
func TestResolveRootKeepsExplicitRootWithoutWorkingDirectory(t *testing.T) {
	explicit := makeRoot(t)
	got, err := resolveRoot(explicit, "/nonexistent/.runtime/luna", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != explicit {
		t.Fatalf("got %q, want the explicit root %q", got, explicit)
	}
}

func TestResolveRootRejectsInvalidExplicitRoot(t *testing.T) {
	invalid := t.TempDir() // exists, but holds neither web/ nor plugins/
	exeRoot := makeRoot(t)
	if _, err := resolveRoot(invalid, filepath.Join(exeRoot, ".runtime", "luna"), exeRoot); err == nil {
		t.Fatal("expected an error for an explicit root without web/ and plugins/")
	}
}

func TestResolveRootPrefersExecutableSibling(t *testing.T) {
	exeRoot := makeRoot(t)
	cwdRoot := makeRoot(t)
	got, err := resolveRoot("", filepath.Join(exeRoot, ".runtime", "luna"), cwdRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != exeRoot {
		t.Fatalf("got %q, want the executable sibling root %q", got, exeRoot)
	}
}

// A `go run ./cmd/luna` invocation places the executable in a temporary build
// directory, so the only usable candidate is the working directory.
func TestResolveRootFallsBackToWorkingDirectory(t *testing.T) {
	cwdRoot := makeRoot(t)
	goRunExe := filepath.Join(t.TempDir(), "go-build123", "b001", "exe", "luna")
	got, err := resolveRoot("", goRunExe, cwdRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != cwdRoot {
		t.Fatalf("got %q, want the working directory %q", got, cwdRoot)
	}
}

func TestResolveRootSkipsEmptyWorkingDirectory(t *testing.T) {
	exeRoot := makeRoot(t)
	got, err := resolveRoot("", filepath.Join(exeRoot, ".runtime", "luna"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != exeRoot {
		t.Fatalf("got %q, want the executable sibling root %q", got, exeRoot)
	}
}

// A relative -root is usable because the process never changes directory.
func TestResolveRootAcceptsRelativeExplicitRoot(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot read the working directory: %v", err)
	}
	if err := os.Chdir(makeRoot(t)); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	got, err := resolveRoot(".", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "." {
		t.Fatalf("got %q, want %q", got, ".")
	}
}

func TestResolveRootErrorNamesTriedPaths(t *testing.T) {
	exeRoot := t.TempDir() // exists, holds neither asset
	cwd := t.TempDir()     // a distinct directory, also without assets
	exe := filepath.Join(exeRoot, ".runtime", "luna")
	_, err := resolveRoot("", exe, cwd)
	if err == nil {
		t.Fatal("expected an error when no candidate holds web/ and plugins/")
	}
	for _, want := range []string{exeRoot, cwd} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name the tried path %q", err, want)
		}
	}
}

// The executable grandparent and the working directory are the same string in
// the common `./.runtime/luna` layout, so the error must not name it twice.
func TestResolveRootDeduplicatesCandidates(t *testing.T) {
	shared := t.TempDir()
	exe := filepath.Join(shared, ".runtime", "luna")
	_, err := resolveRoot("", exe, shared)
	if err == nil {
		t.Fatal("expected an error when the shared candidate holds no assets")
	}
	if count := strings.Count(err.Error(), shared); count != 1 {
		t.Fatalf("error %q names the shared candidate %d times, want 1", err, count)
	}
}

// localData decides where the user's own local data lives. The new location is
// under the XDG data root; the location used before this version is the
// checkout's .runtime/ next to the sessions. Both decisions are made by what is
// actually there, and each one is reported so the startup log can say which
// location is in use and why.
func TestLocalDataUsesTheDataRootWhenTheCheckoutHasNothing(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	sessions, state, notes := localData(data, repo)
	if want := filepath.Join(data, "sessions"); sessions != want {
		t.Fatalf("sessions = %q, want %q", sessions, want)
	}
	if want := data; state != want {
		t.Fatalf("state root = %q, want %q", state, want)
	}
	assertNotes(t, notes, sessions, data)
}

func TestLocalDataKeepsTheCheckoutLocationWhileItHoldsSessions(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	old := filepath.Join(repo, ".runtime", "sessions")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	sessions, state, notes := localData(data, repo)
	if sessions != old {
		t.Fatalf("sessions = %q, want the location that already holds them (%q)", sessions, old)
	}
	// The two decisions are taken independently, and that is the point of asking
	// what is there: this installation has no memory file in the checkout, so its
	// state root goes to the new location while the conversations stay put.
	if state != data {
		t.Fatalf("state root = %q, want %q: nothing of the state is in the checkout", state, data)
	}
	assertNotes(t, notes, old)
}

// The realistic upgrade: an installation that ran before has both the sessions and
// the memory file in the checkout, and keeps both there until the user moves them.
func TestLocalDataKeepsBothInTheCheckoutWhenBothAreThere(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	oldSessions := filepath.Join(repo, ".runtime", "sessions")
	if err := os.MkdirAll(oldSessions, 0o700); err != nil {
		t.Fatal(err)
	}
	oldMemory := filepath.Join(repo, ".runtime", memory.StateFileName)
	if err := os.WriteFile(oldMemory, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, state, notes := localData(data, repo)
	if sessions != oldSessions {
		t.Fatalf("sessions = %q, want %q", sessions, oldSessions)
	}
	if state != repo {
		t.Fatalf("state root = %q, want %q so memory keeps its old file", state, repo)
	}
	assertNotes(t, notes, oldSessions, repo)
}

// The two decisions are independent: a machine can have sessions in the new
// place and facts still in the old one, and each has to say what it did.
func TestLocalDataDecidesSessionsAndStateSeparately(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".runtime", "memory.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, state, _ := localData(data, repo)
	if want := filepath.Join(data, "sessions"); sessions != want {
		t.Fatalf("sessions = %q, want %q: nothing is in the checkout's session directory", sessions, want)
	}
	if state != repo {
		t.Fatalf("state root = %q, want %q: the memory file still lives in the checkout", state, repo)
	}
}

func TestLocalDataPrefersTheDataRootWhenBothLocationsExist(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	for _, dir := range []string{
		filepath.Join(repo, ".runtime", "sessions"),
		filepath.Join(data, "sessions"),
		filepath.Join(data, ".runtime"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(repo, ".runtime", "memory.jsonl"),
		filepath.Join(data, ".runtime", "memory.jsonl"),
	} {
		if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessions, state, _ := localData(data, repo)
	if want := filepath.Join(data, "sessions"); sessions != want {
		t.Fatalf("sessions = %q, want %q", sessions, want)
	}
	if want := data; state != want {
		t.Fatalf("state root = %q, want %q", state, want)
	}
}

// A built binary in .runtime/ is not data: a checkout that was only compiled
// must still start in the new location.
func TestLocalDataIgnoresBuildOutputInTheCheckout(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".runtime", "luna"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessions, state, _ := localData(data, repo)
	if want := filepath.Join(data, "sessions"); sessions != want {
		t.Fatalf("sessions = %q, want %q", sessions, want)
	}
	if want := data; state != want {
		t.Fatalf("state root = %q, want %q", state, want)
	}
}

func TestExplicitFlagsWinOverTheResolvedLocation(t *testing.T) {
	dir, note := explicitOr("/elsewhere/sessions", "/resolved/sessions", "sessions")
	if dir != "/elsewhere/sessions" {
		t.Fatalf("dir = %q, want the explicit value", dir)
	}
	if note == "" {
		t.Fatal("an explicit value should be reported, so the log does not read as if the default applied")
	}
	dir, note = explicitOr("", "/resolved/sessions", "sessions")
	if dir != "/resolved/sessions" || note != "" {
		t.Fatalf("dir = %q note = %q, want the resolved location and no note", dir, note)
	}
}

// assertNotes requires the log lines to name the directory in use, so a user
// reading startup output never has to guess which location was chosen.
func assertNotes(t *testing.T, notes []string, wanted ...string) {
	t.Helper()
	joined := strings.Join(notes, "\n")
	for _, want := range wanted {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes %q should name %q", joined, want)
		}
	}
}

// The capability's own file is what tells the two locations apart, so where that
// file lands is the whole point of the rule: in the new location it sits under
// the data root, and in the old one it keeps the path it has always had.
func TestMemoryFileFollowsTheStateRoot(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	_, state, _ := localData(data, repo)
	dir, err := plugin.StateDirFor(memory.Descriptor(), state)
	if err != nil {
		t.Fatalf("StateDirFor: %v", err)
	}
	if want := filepath.Join(data, ".runtime"); dir != want {
		t.Fatalf("state dir = %q, want %q", dir, want)
	}
	if got, want := filepath.Join(dir, memory.StateFileName), filepath.Join(data, ".runtime", "memory.jsonl"); got != want {
		t.Fatalf("memory file = %q, want %q", got, want)
	}

	// An installation that still keeps facts in the checkout keeps the path it
	// had before this version — byte for byte, so its file is found untouched.
	if err := os.MkdirAll(filepath.Join(repo, ".runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".runtime", memory.StateFileName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, oldState, _ := localData(data, repo)
	oldDir, err := plugin.StateDirFor(memory.Descriptor(), oldState)
	if err != nil {
		t.Fatalf("StateDirFor: %v", err)
	}
	if want := filepath.Join(repo, ".runtime"); oldDir != want {
		t.Fatalf("old state dir = %q, want the path memory had before this version (%q)", oldDir, want)
	}
}

// UI plugins are discovered under the fixed plugins/ui convention, derived from
// the resolved root and never from a request.
func TestUIPluginsDirIsUnderTheResolvedRoot(t *testing.T) {
	got := uiPluginsDir("/repo")
	if want := filepath.Join("/repo", "plugins", "ui"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The rules file is read by the composition root, not by the capability. A file
// it cannot use yields no rules rather than a cut-down set, and the statement
// about it names the file, never the directory it sits in.
func TestLoadRulesReadsTheFileAndReportsProblemsWithoutAHostPath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	limit := workspace.MaxRulesTextBytes

	// A readable file is read and trimmed, with no problem to report.
	good := write("rules.md", "\n  Commit messages are written in English.  \n")
	text, problem := loadRules(good, limit)
	if problem != "" {
		t.Fatalf("a readable file reported %q", problem)
	}
	if text != "Commit messages are written in English." {
		t.Fatalf("rules text=%q, want the trimmed body", text)
	}

	// No file configured is not a problem: the project simply states no rules.
	if text, problem := loadRules("", limit); text != "" || problem != "" {
		t.Fatalf("unconfigured rules: text=%q problem=%q, want both empty", text, problem)
	}

	// A missing file is reported by name, and the report must not carry the
	// absolute host path os.Open echoes back.
	missing := filepath.Join(dir, "missing.md")
	text, problem = loadRules(missing, limit)
	if text != "" || !strings.Contains(problem, "missing.md") || !strings.Contains(problem, "cannot be opened") {
		t.Fatalf("missing file: text=%q problem=%q", text, problem)
	}
	if strings.Contains(problem, dir) {
		t.Fatalf("the report carries the host path %q: %q", dir, problem)
	}

	// An empty file states no rules, and says so.
	empty := write("empty.md", "  \n\t\n")
	text, problem = loadRules(empty, limit)
	if text != "" || !strings.Contains(problem, "empty.md") || !strings.Contains(problem, "is empty") {
		t.Fatalf("empty file: text=%q problem=%q", text, problem)
	}

	// The boundary is exact: exactly the ceiling is read whole, one byte more is
	// refused whole rather than cut to fit.
	atLimit := write("at-limit.md", strings.Repeat("a", limit))
	text, problem = loadRules(atLimit, limit)
	if problem != "" || len(text) != limit {
		t.Fatalf("at the ceiling: len(text)=%d problem=%q, want %d and no problem", len(text), problem, limit)
	}
	over := write("over.md", strings.Repeat("a", limit+1))
	text, problem = loadRules(over, limit)
	if text != "" || !strings.Contains(problem, "over.md") || !strings.Contains(problem, "larger than") {
		t.Fatalf("over the ceiling: len(text)=%d problem=%q", len(text), problem)
	}
	if strings.Contains(problem, dir) {
		t.Fatalf("the report carries the host path %q: %q", dir, problem)
	}
}

// The ceiling the composition root reads a rules file with is the capability's
// own: a file the capability would refuse to state is never loaded whole. It is
// derived from the block budget, so it stays below it.
func TestTheRulesReadCeilingIsTheCapabilitysOwn(t *testing.T) {
	if workspace.MaxRulesTextBytes <= 0 || workspace.MaxRulesTextBytes >= workspace.RulesBudgetBytes {
		t.Fatalf("rules text ceiling=%d, want a positive value below the block budget %d",
			workspace.MaxRulesTextBytes, workspace.RulesBudgetBytes)
	}
}

// An explicit choice is never second-guessed, and the default is the project's own
// rules file rather than nothing: a project agent that ignores the file its project
// keeps its rules in is not reading the project. Neither case is an error.
func TestProjectRulesDefaultAndExplicitSelection(t *testing.T) {
	root := t.TempDir()
	if text, problem := loadProjectRules(root, "", workspace.MaxRulesTextBytes); text != "" || problem != "" {
		t.Fatalf("missing rules: %d bytes, %q", len(text), problem)
	}
	rules := filepath.Join(root, defaultRulesName)
	if err := os.WriteFile(rules, []byte("project rules"), 0600); err != nil {
		t.Fatal(err)
	}
	if text, problem := loadProjectRules(root, "", workspace.MaxRulesTextBytes); text != "project rules" || problem != "" {
		t.Fatalf("default selection: %q", problem)
	}
	explicit := filepath.Join(t.TempDir(), "chosen.md")
	if err := os.WriteFile(explicit, []byte("explicit rules"), 0600); err != nil {
		t.Fatal(err)
	}
	if text, problem := loadProjectRules(root, explicit, workspace.MaxRulesTextBytes); text != "explicit rules" || problem != "" {
		t.Fatalf("explicit selection: %q", problem)
	}
	directoryRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(directoryRoot, defaultRulesName), 0700); err != nil {
		t.Fatal(err)
	}
	if text, problem := loadProjectRules(directoryRoot, "", workspace.MaxRulesTextBytes); text != "" || problem == "" {
		t.Fatalf("a directory must be reported, not treated as a rule file: %q", problem)
	}
}

func TestWorkingDirOrEmptyWhenDirectoryIsRemoved(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot read the working directory: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.Remove(dir); err != nil {
		t.Fatalf("remove the working directory: %v", err)
	}
	if got := workingDirOrEmpty(); got != "" {
		t.Fatalf("got %q, want an empty string when the working directory is gone", got)
	}
}

// The user's configuration file has one default location, under the user's own
// configuration directory, and -config-file overrides it. Getting this wrong
// would mean a user edits a file Luna never reads.
func TestConfigFileDefaultsToTheUserConfigDirectory(t *testing.T) {
	configDir := "/home/someone/.config/luna"
	if got, want := configFileFor("", configDir), filepath.Join(configDir, config.FileName); got != want {
		t.Fatalf("default = %q, want %q", got, want)
	}
	if got := configFileFor("/tmp/mine.yaml", configDir); got != "/tmp/mine.yaml" {
		t.Fatalf("an explicit file must win, got %q", got)
	}
}

// A missing file is the state most installations are in, so it must not be
// reported as a problem; a file that exists is reported as read.
func TestAMissingUserConfigIsNotAProblem(t *testing.T) {
	file, err := userConfig(filepath.Join(t.TempDir(), config.FileName))
	if err != nil {
		t.Fatalf("a file that is not there is not an error: %v", err)
	}
	if file != nil {
		t.Fatalf("file = %#v, want nil", file)
	}

	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte("max_iterations: 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err = userConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if file == nil || file.MaxIterations != 12 {
		t.Fatalf("file = %#v, want the file that was there", file)
	}
}

// The roots discovery reads are the user's own directories: the one under the
// data root, plus whatever -skills-dir added. The working directory and the
// resolved installation root are not roots — where Luna was started must not
// change which skills exist.
func TestSkillRootsAreTheUsersOwnDirectories(t *testing.T) {
	paths := layout.Paths{Install: "/srv/luna", Data: "/home/someone/.local/share/luna"}
	extra := repeatedPath{"/home/someone/team-skills", "/tmp/one-off"}

	roots := skillRoots(paths, extra)
	want := []skills.Root{
		{Path: "/home/someone/.local/share/luna/skills", Scope: skills.ScopeUser},
		{Path: "/home/someone/team-skills", Scope: skills.ScopeUser},
		{Path: "/tmp/one-off", Scope: skills.ScopeUser},
	}
	if len(roots) != len(want) {
		t.Fatalf("roots=%+v, want %+v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("roots[%d]=%+v, want %+v", i, roots[i], want[i])
		}
	}
	for _, root := range roots {
		if strings.Contains(root.Path, paths.Install) {
			t.Fatalf("root %q comes from the installation", root.Path)
		}
	}
}

func TestSkillsDirMayBeRepeatedAndRejectsAnEmptyValue(t *testing.T) {
	var dirs repeatedPath
	for _, value := range []string{"/one", " /two "} {
		if err := dirs.Set(value); err != nil {
			t.Fatalf("set %q: %v", value, err)
		}
	}
	if len(dirs) != 2 || dirs[0] != "/one" || dirs[1] != "/two" {
		t.Fatalf("dirs=%v, want two trimmed entries in order", dirs)
	}
	if err := dirs.Set("   "); err == nil {
		t.Fatal("an empty directory was accepted")
	}
	if len(dirs) != 2 {
		t.Fatalf("dirs=%v, want the rejected value left out", dirs)
	}
	if !strings.Contains(dirs.String(), "/one") {
		t.Fatalf("String()=%q, want it to report what was set", dirs.String())
	}
}

// toolsOf 是注册表此刻能让模型看到的工具名：只有启用中的能力贡献的工具算数。
func toolsOf(registry *plugin.Registry) []string {
	var names []string
	for _, entry := range registry.Enabled() {
		provider, ok := entry.Plugin.(plugin.ToolProvider)
		if !ok {
			continue
		}
		for _, tool := range provider.Tools() {
			names = append(names, tool.Name())
		}
	}
	return names
}

// 命令执行默认关闭：能力始终注册（否则设置页没有可打开的东西），但只有用户在设置里显式
// 打开时才启用。没有 process.exec 授权的注册表连注册都不接受——内置不是特权。
func TestTheTerminalCapabilityIsOffUntilTheSettingsSaySo(t *testing.T) {
	t.Run("a capability nobody asked for is not running", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionProcessExec)
		if err := registerTerminal(registry, false); err != nil {
			t.Fatal(err)
		}
		entry, found := registry.Entry(terminal.PluginID)
		if !found {
			t.Fatal("the terminal capability was not registered, so the settings page has nothing to turn on")
		}
		if entry.State != plugin.StateRegistered {
			t.Fatalf("state = %q, want %q", entry.State, plugin.StateRegistered)
		}
		if tools := toolsOf(registry); slices.Contains(tools, terminal.RunToolName) {
			t.Fatalf("tools = %v, want no %s", tools, terminal.RunToolName)
		}
	})
	t.Run("the user's choice turns it on", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionProcessExec)
		if err := registerTerminal(registry, true); err != nil {
			t.Fatal(err)
		}
		entry, _ := registry.Entry(terminal.PluginID)
		if entry.State != plugin.StateEnabled {
			t.Fatalf("state = %q, want %q", entry.State, plugin.StateEnabled)
		}
		if tools := toolsOf(registry); !slices.Contains(tools, terminal.RunToolName) {
			t.Fatalf("tools = %v, want %s", tools, terminal.RunToolName)
		}
	})
	t.Run("a registry that did not grant it refuses it", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite)
		if err := registerTerminal(registry, true); err == nil {
			t.Fatal("a registry that did not grant process execution accepted the terminal capability")
		}
	})
}

// 用户在设置页做的选择写进 settings.yaml，重启后仍然生效；写盘失败时报错且一个字节没改，
// 于是设置页可以把这次请求当成没发生过。
func TestACapabilityChoiceIsRecordedWithoutLosingTheOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "luna", settings.FileName)
	prefs := newUserPreferences(nil, path, settings.Settings{Skills: settings.Skills{Disabled: []string{"alpha"}}})

	if err := prefs.SetEnabled(terminal.PluginID, true); err != nil {
		t.Fatal(err)
	}
	// 第二次选择不是从磁盘重读，而是接着上一次的结果：同一进程里连续两次选择都要留住。
	if err := prefs.SetEnabled("another-capability", true); err != nil {
		t.Fatal(err)
	}
	file, _, err := settings.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !file.CapabilityEnabled(terminal.PluginID) || !file.CapabilityEnabled("another-capability") {
		t.Fatalf("enabled = %v, want both choices", file.EnabledCapabilities())
	}
	if names := file.DisabledSkills(); len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("disabled skills = %v, want the skill choice kept", names)
	}

	// 写不进去时必须报告失败：一个目录占着文件的位置，整文件重写到这里没有人能读懂。
	blocked := filepath.Join(t.TempDir(), "luna", settings.FileName)
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	blockedPrefs := newUserPreferences(nil, blocked, settings.Settings{})
	if err := blockedPrefs.SetEnabled(terminal.PluginID, true); err == nil {
		t.Fatal("a choice that could not be written was reported as recorded")
	}
}

// 写文件的能力同样是「不自己跑」的那一类：注册了但没启用，直到用户在设置里打开它；
// 而且即使打开了，它每次调用都会去读用户允许写入的目录，零个目录时任何写入都被拒。
func TestTheFileWriteCapabilityIsOffUntilTheSettingsSaySo(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), settings.FileName)

	t.Run("a capability nobody asked for is not running", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionFilesystemWrite)
		if err := registerFileWrite(registry, settingsPath, false); err != nil {
			t.Fatal(err)
		}
		entry, found := registry.Entry(filewrite.PluginID)
		if !found {
			t.Fatal("the file write capability was not registered, so the settings page has nothing to turn on")
		}
		if entry.State != plugin.StateRegistered {
			t.Fatalf("state = %q, want %q", entry.State, plugin.StateRegistered)
		}
		if tools := toolsOf(registry); slices.Contains(tools, filewrite.WriteToolName) {
			t.Fatalf("tools = %v, want no %s", tools, filewrite.WriteToolName)
		}
	})
	t.Run("the user's choice turns it on", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionFilesystemWrite)
		if err := registerFileWrite(registry, settingsPath, true); err != nil {
			t.Fatal(err)
		}
		if tools := toolsOf(registry); !slices.Contains(tools, filewrite.WriteToolName) {
			t.Fatalf("tools = %v, want %s", tools, filewrite.WriteToolName)
		}
	})
	t.Run("a registry that did not grant it refuses it", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite)
		if err := registerFileWrite(registry, settingsPath, true); err == nil {
			t.Fatal("a registry that did not grant filesystem writes accepted the file write capability")
		}
	})
}

// 抓网页的能力跟前面两个一样是「不自己跑」的那一类：注册了但没启用，直到用户在设置里
// 打开它；没授权 net.fetch 的注册表必须拒它。
func TestTheWebCapabilityIsOffUntilTheSettingsSaySo(t *testing.T) {
	t.Run("a capability nobody asked for is not running", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionNetworkFetch)
		if err := registerWeb(registry, false); err != nil {
			t.Fatal(err)
		}
		entry, found := registry.Entry(web.PluginID)
		if !found {
			t.Fatal("the web capability was not registered, so the settings page has nothing to turn on")
		}
		if entry.State != plugin.StateRegistered {
			t.Fatalf("state = %q, want %q", entry.State, plugin.StateRegistered)
		}
		if tools := toolsOf(registry); slices.Contains(tools, web.FetchToolName) {
			t.Fatalf("tools = %v, want no %s", tools, web.FetchToolName)
		}
	})
	t.Run("the user's choice turns it on", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionNetworkFetch)
		if err := registerWeb(registry, true); err != nil {
			t.Fatal(err)
		}
		if tools := toolsOf(registry); !slices.Contains(tools, web.FetchToolName) {
			t.Fatalf("tools = %v, want %s", tools, web.FetchToolName)
		}
	})
	t.Run("a registry that did not grant it refuses it", func(t *testing.T) {
		registry := plugin.NewRegistry(plugin.PermissionStateWrite)
		if err := registerWeb(registry, true); err == nil {
			t.Fatal("a registry that did not grant network fetches accepted the web capability")
		}
	})
}

// 设置页提交的是整份列表：不在里面的目录就是不再被允许，所以撤销一个目录不需要记得是
// 哪一次请求加进来的。写盘失败时一个字节不该改。
func TestTheWholeWriteDirectoryListIsReplacedAndRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "luna", settings.FileName)
	prefs := newUserPreferences(nil, path, settings.Settings{})
	if err := prefs.SetDirs([]string{"/one", "/two"}); err != nil {
		t.Fatal(err)
	}
	file, _, err := settings.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirs := file.WriteDirs(); len(dirs) != 2 || dirs[0] != "/one" || dirs[1] != "/two" {
		t.Fatalf("dirs = %v, want [/one /two]", dirs)
	}
	// 整份替换：第二个被去掉，不是被追加。
	if err := prefs.SetDirs([]string{"/one"}); err != nil {
		t.Fatal(err)
	}
	stored, err := prefs.Dirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0] != "/one" {
		t.Fatalf("dirs = %v, want [/one]", stored)
	}
	// 写不进去时报告失败，且内存里那份也不改。
	blocked := filepath.Join(t.TempDir(), "luna", settings.FileName)
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	brokenPrefs := newUserPreferences(nil, blocked, settings.Settings{})
	if err := brokenPrefs.SetDirs([]string{"/one"}); err == nil {
		t.Fatal("a list that could not be written was reported as recorded")
	}
	if dirs, err := brokenPrefs.Dirs(); err != nil || len(dirs) != 0 {
		t.Fatalf("dirs = %v (err %v), want nothing recorded", dirs, err)
	}
}

// The write deadline has to sit above the run budget: while it was the shorter of the two
// (a fixed 70 seconds against a 15-minute budget), a run that streamed past 70 seconds was
// cut at the socket — the client got no terminal event, and the dead connection cancelled
// the run, so the budget never got to end it.
func TestWriteDeadlineSitsAboveTheRunBudget(t *testing.T) {
	budgets := []time.Duration{httpapi.DefaultRunTimeout, 90 * time.Second, 20 * time.Minute, time.Hour}
	for _, budget := range budgets {
		if got := writeDeadlineFor(budget); got <= budget {
			t.Fatalf("write deadline %s must exceed the run budget %s", got, budget)
		}
	}
}
