package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testHost(t *testing.T, opts Options) *Host {
	t.Helper()
	h, err := New(context.Background(), testRoot(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

// slowCallOptions is for the two tests that keep a tool call in flight while a
// reload publishes the next generation. Both have to observe a call that is
// still running when the replacement lands, and a reload builds and starts a
// candidate for every tool on the allowlist: measured under the gate's own
// parallel run with the race detector, that takes about three seconds even with
// the builds warmed. The defaults are sized for a tool call rather than for a
// test racing one against a reload — a five second RPC timeout leaves no room to
// observe anything at all — so these tests state the timeouts they need instead
// of inheriting them. Nothing here changes the defaults other tests rely on.
func slowCallOptions() Options {
	return Options{
		BuildTimeout: 120 * time.Second,
		StartTimeout: 60 * time.Second,
		RPCTimeout:   60 * time.Second,
	}
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}

// active returns the active record of one tool, failing when it is absent.
func active(t *testing.T, h *Host, tool string) *Record {
	t.Helper()
	record := h.State().Active(tool)
	if record == nil {
		t.Fatalf("no active generation for %s: %+v", tool, h.State().Plugins)
	}
	return record
}

// Every allowlisted tool must start, be reported by state, and run as its own
// process. Two tools built from the same directory would make this test check
// one string twice.
func TestEveryAllowlistedToolStartsInItsOwnProcess(t *testing.T) {
	h := testHost(t, Options{})
	state := h.State()
	if len(state.Plugins) != len(Allowlist) {
		t.Fatalf("plugins=%+v, want one record per allowlisted tool", state.Plugins)
	}
	seen := map[int]string{}
	for _, spec := range Allowlist {
		record := state.Active(spec.Tool)
		if record == nil {
			t.Fatalf("%s has no active generation: %+v", spec.Tool, state.Plugins)
		}
		if record.Version != "1.0.0" || record.Generation != 1 {
			t.Fatalf("%s: %+v", spec.Tool, record)
		}
		if record.PluginPID <= 0 || record.PluginPID == os.Getpid() {
			t.Fatalf("%s must run out of process: %+v", spec.Tool, record)
		}
		if other, dup := seen[record.PluginPID]; dup {
			t.Fatalf("%s and %s share plugin pid %d", spec.Tool, other, record.PluginPID)
		}
		seen[record.PluginPID] = spec.Tool
	}

}

func TestRealSubprocessReplacementPinsInflightAndRollsBack(t *testing.T) {
	h := testHost(t, slowCallOptions())
	// Warm every tool family's candidate builds before the timed section below. A
	// reload builds a candidate for each tool on the allowlist, and a cold build
	// can outlast the in-flight window, so the call finishes first and the
	// assertion that follows fails for a reason that has nothing to do with
	// pinning. This test is about pinning a call to the generation that started
	// it, not about how long a build takes; reloading to v2 and back leaves the
	// host in the state the rest of the test expects.
	if err := reloadFixture(t, h, "updated"); err != nil {
		t.Fatal(err)
	}
	if err := reloadFixture(t, h, "baseline"); err != nil {
		t.Fatal(err)
	}
	first := *active(t, h, ToolTextTransform)
	if first.Version != "1.0.0" || first.PluginPID == os.Getpid() {
		t.Fatalf("bad v1 state: %+v", first)
	}
	if reader := active(t, h, ToolReadFile); reader.Version != "1.0.0" {
		t.Fatalf("bad reader state: %+v", reader)
	}

	// Keep the old RPC in flight longer than a reload takes. Ten seconds is about
	// three times the slowest reload measured under the gate's parallel run, and
	// it is the reason this test asks for its own RPC timeout: the five second
	// default is shorter than the window, so a call that outlives a reload would
	// be killed by the timeout instead of pinned.
	const inFlightMS = 10000
	done := make(chan Output, 1)
	errs := make(chan error, 1)
	go func() {
		out, err := h.Invoke(context.Background(), Input{Text: " first ", DelayMS: inFlightMS})
		done <- out
		errs <- err
	}()
	waitFor(t, func() bool { a := h.State().Active(ToolTextTransform); return a != nil && a.Inflight == 1 })
	reloadStarted := time.Now()
	if err := reloadFixture(t, h, "updated"); err != nil {
		t.Fatal(err)
	}
	t.Logf("timed reload took %v (in-flight window is %dms)", time.Since(reloadStarted), inFlightMS)
	second := *active(t, h, ToolTextTransform)
	if second.Version != "2.0.0" || second.Generation == first.Generation || second.PluginPID == first.PluginPID {
		t.Fatalf("bad replacement: %+v", second)
	}
	// The pinned transform generation is retained; the reader's old generation
	// had nothing in flight and is already gone. One record per tool, plus the one
	// generation that is still serving the call it started.
	if len(h.State().Plugins) != len(Allowlist)+1 {
		t.Fatalf("old generation not retained exactly once: %+v", h.State().Plugins)
	}
	if reader := active(t, h, ToolReadFile); reader.Version != "2.0.0" || reader.Generation != second.Generation {
		t.Fatalf("reader was not replaced with the same generation: %+v", reader)
	}
	out2, err := h.Invoke(context.Background(), Input{Text: " hello ", DelayMS: 0})
	if err != nil || out2.Result != "Luna · HELLO" || out2.Generation != second.Generation {
		t.Fatalf("v2 output=%+v err=%v", out2, err)
	}
	out1 := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if out1.Result != "first" || out1.Generation != first.Generation || out1.PluginPID != first.PluginPID {
		t.Fatalf("lost pin: %+v", out1)
	}
	waitFor(t, func() bool {
		return len(h.State().Plugins) == len(Allowlist) && syscall.Kill(first.PluginPID, 0) == syscall.ESRCH
	})
	before := map[string]Record{}
	// 白名单里每个工具都取一份，不写死名字：加一个工具时这条测试要问的仍然是
	// “重载失败后每个工具的代次都没被换掉”。
	for _, spec := range Allowlist {
		before[spec.Tool] = *active(t, h, spec.Tool)
	}
	if err := reloadFixture(t, h, "reject-handshake"); err == nil {
		t.Fatal("broken candidate accepted")
	}
	for _, tool := range []string{ToolTextTransform, ToolReadFile} {
		after := *active(t, h, tool)
		if after.Generation != before[tool].Generation || after.PluginPID != before[tool].PluginPID {
			t.Fatalf("rollback changed %s: before=%+v after=%+v", tool, before[tool], after)
		}
	}
}

func TestSameVersionReloadCreatesNewProcessAndCleansOld(t *testing.T) {
	h := testHost(t, Options{})
	old := *active(t, h, ToolTextTransform)
	if err := reloadFixture(t, h, "baseline"); err != nil {
		t.Fatal(err)
	}
	cur := active(t, h, ToolTextTransform)
	if cur.Generation == old.Generation || cur.PluginPID == old.PluginPID {
		t.Fatalf("same version was not rebuilt: old=%+v new=%+v", old, cur)
	}
	waitFor(t, func() bool {
		return syscall.Kill(old.PluginPID, 0) == syscall.ESRCH && len(h.State().Plugins) == len(Allowlist)
	})
}

func TestInvokeTimeoutTerminatesOnlyTheOwnedPlugin(t *testing.T) {
	h := testHost(t, Options{RPCTimeout: 100 * time.Millisecond})
	pid := active(t, h, ToolTextTransform).PluginPID
	reader := *active(t, h, ToolReadFile)
	_, err := h.Invoke(context.Background(), Input{Text: "x", DelayMS: 500})
	if err == nil {
		t.Fatal("expected timeout")
	}
	waitFor(t, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH && h.State().Active(ToolTextTransform) == nil
	})
	// The other tool is a separate process and keeps serving.
	if got := active(t, h, ToolReadFile); got.PluginPID != reader.PluginPID || got.Generation != reader.Generation {
		t.Fatalf("reader was disturbed: %+v", got)
	}
}

func TestRejectsUnknownReloadTarget(t *testing.T) {
	h := testHost(t, Options{})
	for _, candidate := range []string{"../../bin/sh", "read_file/v1", "/plugins/v1", "v3", " luna_read_file"} {
		if err := h.Reload(context.Background(), candidate); err == nil {
			t.Fatalf("unknown candidate %q accepted", candidate)
		}
	}
	if got := active(t, h, ToolTextTransform); got.Version != "1.0.0" {
		t.Fatalf("rejected candidates changed the active tool: %+v", got)
	}
}

// A tool name is an allowlist lookup, so a browser- or model-supplied string
// can never become a build path or a dispatch target.
func TestUnknownToolIsNotInvokable(t *testing.T) {
	h := testHost(t, Options{})
	for _, tool := range []string{"../../plugins/v1", "luna_shell", "", ToolTextTransform + " "} {
		_, err := h.invoke(context.Background(), tool, Input{Text: "x"})
		if err == nil {
			t.Fatalf("unknown tool %q was invoked", tool)
		}
		if !errors.Is(err, ErrUnknownTool) {
			t.Fatalf("unknown tool %q error = %v, want ErrUnknownTool", tool, err)
		}
	}
}

// A plugin that dies mid-call must be reported as infrastructure, not as a
// refusal: nothing about the call was wrong, the process serving it was gone.
func TestAPluginThatDiesMidCallIsReportedAsInfrastructure(t *testing.T) {
	h := testHost(t, Options{})
	record := active(t, h, ToolTextTransform)
	proc, err := os.FindProcess(record.PluginPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := h.Invoke(context.Background(), Input{Text: "hi"})
		if errors.Is(err, ErrPluginGone) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("invoke after killing plugin pid %d = %v, want ErrPluginGone", record.PluginPID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A tool with no active generation is infrastructure too: the call never
// reached a plugin, so nothing about it can be reported as a refusal.
func TestAClosedHostReportsInfrastructure(t *testing.T) {
	h, err := New(context.Background(), testRoot(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	if _, err := h.Invoke(context.Background(), Input{Text: "hi"}); !errors.Is(err, ErrNoActivePlugin) {
		t.Fatalf("invoke on a closed host = %v, want ErrNoActivePlugin", err)
	}
}

func TestReadFileReturnsExactContentAndSurvivesReplacement(t *testing.T) {
	readRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(readRoot, "notes.txt"), []byte("line one\r\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, Options{ReadRoot: readRoot, ReadLimit: 4096})

	out, err := h.ReadFile(context.Background(), ReadRequest{Path: "notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != "line one\r\nline two\n" {
		t.Fatalf("v1 must return content verbatim, got %q", out.Result)
	}
	if out.Version != "1.0.0" || out.Generation != 1 || out.PluginPID <= 0 {
		t.Fatalf("metadata lost: %+v", out)
	}
	if transformer := active(t, h, ToolTextTransform); transformer.PluginPID == out.PluginPID {
		t.Fatal("reader and transformer share one process")
	}

	if err := reloadFixture(t, h, "updated"); err != nil {
		t.Fatal(err)
	}
	replaced, err := h.ReadFile(context.Background(), ReadRequest{Path: "notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Result != "line one\nline two\n" {
		t.Fatalf("v2 must normalize line endings, got %q", replaced.Result)
	}
	if replaced.Generation == out.Generation || replaced.PluginPID == out.PluginPID {
		t.Fatalf("reader replacement did not create a new generation: before=%+v after=%+v", out, replaced)
	}
}

// The host validates the path before any RPC, so a rejected read never reaches
// a plugin process.
func TestReadFileRejectionsHappenOnTheHostSide(t *testing.T) {
	readRoot := t.TempDir()
	secret := t.TempDir()
	if err := os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(secret, "secret.txt"), filepath.Join(readRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readRoot, "blob"), []byte("text\x00binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readRoot, "long.txt"), []byte(strings.Repeat("x", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, Options{ReadRoot: readRoot, ReadLimit: 16})

	cases := []struct {
		name string
		path string
		// want is the sentinel for a refusal the host makes before any RPC.
		want error
		// wantText covers a refusal the plugin makes itself: an error raised
		// inside the plugin crosses net/rpc as a string, so its text is the only
		// thing that can be asserted here. The sentinel identity for those
		// checks is covered by the in-package fileread tests.
		wantText string
	}{
		{"empty", "", fileread.ErrPathEmpty, ""},
		{"absolute", "/etc/passwd", fileread.ErrPathAbsolute, ""},
		{"escape", "../secret.txt", fileread.ErrPathEscape, ""},
		{"symlink escape", "escape", fileread.ErrSymlinkEscape, ""},
		{"missing", "absent.txt", fileread.ErrNotFound, ""},
		{"directory", ".", fileread.ErrNotRegular, ""},
		{"over the read limit", "long.txt", fileread.ErrTooLarge, ""},
		{"binary content", "blob", nil, fileread.ErrBinary.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := h.ReadFile(context.Background(), ReadRequest{Path: tc.path})
			if err == nil {
				t.Fatalf("ReadFile(%q) = %+v, want an error", tc.path, out)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("ReadFile(%q) error = %v, want %v", tc.path, err, tc.want)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("ReadFile(%q) error = %v, want it to mention %q", tc.path, err, tc.wantText)
			}
			if out.Result != "" {
				t.Fatalf("a rejected read returned content: %q", out.Result)
			}
			if strings.Contains(err.Error(), readRoot) || strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks an absolute host path: %v", err)
			}
		})
	}
	// A path that climbs but stays inside is not an escape.
	if err := os.WriteFile(filepath.Join(readRoot, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := h.ReadFile(context.Background(), ReadRequest{Path: "sub/../ok.txt"}); err != nil || out.Result != "ok" {
		t.Fatalf("inside .. path rejected: out=%+v err=%v", out, err)
	}
}

// The read root is configuration: a read inside the repository root is refused
// once the root points somewhere else.
func TestReadRootDefaultsToTheRepositoryRoot(t *testing.T) {
	h := testHost(t, Options{})
	out, err := h.ReadFile(context.Background(), ReadRequest{Path: "docs/architecture.md"})
	if err != nil {
		t.Fatalf("default read root cannot read the repository: %v", err)
	}
	// 断言读取的是仓库里那个文件本身，而不是它的开头一行写死在测试里：
	// 后者会让这条测试随文档语言变化而失败，与读取根目录无关。
	want, err := os.ReadFile(filepath.Join(testRoot(t), "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Result, string(want)) {
		t.Fatalf("content differs from the file on disk: %.60q", out.Result)
	}
	narrowed := testHost(t, Options{ReadRoot: t.TempDir()})
	if _, err := narrowed.ReadFile(context.Background(), ReadRequest{Path: "docs/architecture.md"}); !errors.Is(err, fileread.ErrNotFound) {
		t.Fatalf("narrowed read root still reached the repository: %v", err)
	}
}

// The search mode crosses two hops the fileread tests cannot see: the host validates it
// and hands it to the plugin, and the plugin hands it to the same search the literal
// default uses. The query below is chosen so that the two readings disagree — "a.c" is a
// literal miss against a file holding "abc" and a pattern hit — so a plugin that ignored
// the mode could not produce the match, and a host that never validated it could not
// produce the refusal.
func TestSearchFilesCarriesTheModeAcrossThePluginBoundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, Options{ReadRoot: root, ReadLimit: 4096})
	ctx := context.Background()

	// A call that names no mode reads the query literally, exactly as before.
	out, err := h.SearchFiles(ctx, SearchRequest{Path: ".", Query: "a.c"})
	if err != nil {
		t.Fatalf("literal search: %v", err)
	}
	if !strings.Contains(out.Result, "no matches") {
		t.Fatalf("a call with no mode was not read literally: %q", out.Result)
	}

	// The same query with the mode named does match, and the result says how it was read.
	out, err = h.SearchFiles(ctx, SearchRequest{Path: ".", Query: "a.c", Mode: "regex"})
	if err != nil {
		t.Fatalf("regex search: %v", err)
	}
	if !strings.Contains(out.Result, "notes.md") {
		t.Fatalf("the mode did not reach the search: %q", out.Result)
	}
	if !strings.Contains(out.Result, "as a regular expression") {
		t.Fatalf("the result does not say the query was read as a pattern: %q", out.Result)
	}

	// A mode this search does not have is refused here, before any plugin is asked.
	if _, err := h.SearchFiles(ctx, SearchRequest{Path: ".", Query: "a", Mode: "glob"}); !errors.Is(err, fileread.ErrModeInvalid) {
		t.Fatalf("search with an unknown mode err = %v, want ErrModeInvalid", err)
	}

	// A pattern that cannot compile is the call's own refusal, carrying Go's explanation,
	// and never a result that reads as "there is nothing there".
	out, err = h.SearchFiles(ctx, SearchRequest{Path: ".", Query: "[", Mode: "regex"})
	if err == nil {
		t.Fatalf("an uncompilable pattern was answered with %q", out.Result)
	}
	if !strings.Contains(err.Error(), "error parsing regexp") {
		t.Fatalf("the refusal does not carry Go's own explanation: %v", err)
	}
}

// Both new arguments cross the candidate boundary. A candidate that dropped one would answer
// the call as though it had not been given at all, and every other test here runs v1, so this
// switches to v2 and asks again. What is being checked is the argument, not the rendering: v2
// renders sizes in exact bytes and trims indentation on purpose.
func TestUpdatedImplementationReceivesTheSameArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep", "notes.md"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, Options{ReadRoot: root, ReadLimit: 4096})
	if err := reloadFixture(t, h, "updated"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	out, err := h.ListDir(ctx, ListRequest{Path: ".", Depth: 3})
	if err != nil {
		t.Fatalf("listing three levels deep on v2: %v", err)
	}
	if !strings.Contains(out.Result, filepath.Join("sub", "deep", "notes.md")) {
		t.Fatalf("the v2 candidate did not list as deep as it was told to: %q", out.Result)
	}

	out, err = h.SearchFiles(ctx, SearchRequest{Path: ".", Query: "a.c", Mode: "regex"})
	if err != nil {
		t.Fatalf("regex search on v2: %v", err)
	}
	if !strings.Contains(out.Result, "notes.md") {
		t.Fatalf("the v2 candidate did not read the query as a pattern: %q", out.Result)
	}
}

// A file call is checked against the roots that call names, not against whatever
// the host was built with: the working directories of the run decide what the
// model may reach. A call that names none keeps the configured root, which is
// what makes every run that works in no particular directory behave as before.
func TestFileCallsUseTheRootsTheyName(t *testing.T) {
	// The three directories are siblings under one temporary parent, so a `..`
	// from one root really can reach another: that is the shape a workspace
	// with several directories has, and the one an escape check is easiest to
	// get wrong in.
	defaultRoot := t.TempDir()
	a := t.TempDir()
	b := t.TempDir()
	for _, f := range []struct{ dir, name, content string }{
		{defaultRoot, "default.txt", "configured root\n"},
		{a, "a.txt", "from a\n"},
		{b, "b.txt", "from b\n"},
	} {
		if err := os.WriteFile(filepath.Join(f.dir, f.name), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := testHost(t, Options{ReadRoot: defaultRoot, ReadLimit: 4096})
	ctx := context.Background()

	// Every named root is reachable, whichever one holds the path.
	for _, tc := range []struct {
		path string
		want string
	}{{"a.txt", "from a\n"}, {"b.txt", "from b\n"}} {
		out, err := h.ReadFile(ctx, ReadRequest{Path: tc.path, Roots: []string{a, b}})
		if err != nil {
			t.Fatalf("read %q with roots a and b: %v", tc.path, err)
		}
		if out.Result != tc.want {
			t.Fatalf("read %q = %q, want %q", tc.path, out.Result, tc.want)
		}
	}
	// The listing and the search are checked against the same roots.
	if out, err := h.ListDir(ctx, ListRequest{Path: ".", Roots: []string{b}}); err != nil || !strings.Contains(out.Result, "b.txt") {
		t.Fatalf("list of root b = %q, err = %v", out.Result, err)
	}
	if out, err := h.SearchFiles(ctx, SearchRequest{Path: ".", Query: "from a", Roots: []string{a}}); err != nil || !strings.Contains(out.Result, "a.txt") {
		t.Fatalf("search under root a = %q, err = %v", out.Result, err)
	}
	if out, err := h.FindFiles(ctx, FindRequest{Path: ".", Pattern: "*.txt", Roots: []string{a}}); err != nil {
		t.Fatalf("find under root a: %v", err)
	} else if !strings.Contains(out.Result, "a.txt") || strings.Contains(out.Result, "b.txt") {
		t.Fatalf("find under root a = %q, want a.txt and not b.txt", out.Result)
	}

	// A root that is not named is not reachable, even when another root is.
	if out, err := h.ReadFile(ctx, ReadRequest{Path: "b.txt", Roots: []string{a}}); !errors.Is(err, fileread.ErrNotFound) {
		t.Fatalf("read of b.txt with only root a = %q, err = %v, want ErrNotFound", out.Result, err)
	}
	if out, err := h.ListDir(ctx, ListRequest{Path: ".", Roots: []string{a}}); err != nil {
		t.Fatalf("list of root a: %v", err)
	} else if strings.Contains(out.Result, "b.txt") {
		t.Fatalf("listing root a named a file of root b: %q", out.Result)
	}

	// A `..` out of one named root into another is refused: roots are tried in
	// order, and the escape is not something a later root can legalize.
	if out, err := h.ReadFile(ctx, ReadRequest{Path: "../" + filepath.Base(b) + "/b.txt", Roots: []string{a, b}}); !errors.Is(err, fileread.ErrPathEscape) {
		t.Fatalf("read through a sibling root = %q, err = %v, want ErrPathEscape", out.Result, err)
	}
	if _, err := h.SearchFiles(ctx, SearchRequest{Path: "../" + filepath.Base(a), Query: "x", Roots: []string{a, b}}); !errors.Is(err, fileread.ErrPathEscape) {
		t.Fatalf("search through a sibling root err = %v, want ErrPathEscape", err)
	}
	if _, err := h.FindFiles(ctx, FindRequest{Path: "../" + filepath.Base(a), Pattern: "*", Roots: []string{a, b}}); !errors.Is(err, fileread.ErrPathEscape) {
		t.Fatalf("find through a sibling root err = %v, want ErrPathEscape", err)
	}
	// An absolute path is refused however many roots are named, including one
	// that names a file a root really holds.
	if _, err := h.ReadFile(ctx, ReadRequest{Path: filepath.Join(b, "b.txt"), Roots: []string{a, b}}); !errors.Is(err, fileread.ErrPathAbsolute) {
		t.Fatalf("absolute path err = %v, want ErrPathAbsolute", err)
	}

	// No roots named at all is the configured root, unchanged.
	if out, err := h.ReadFile(ctx, ReadRequest{Path: "default.txt"}); err != nil || out.Result != "configured root\n" {
		t.Fatalf("read with no roots = %q, err = %v", out.Result, err)
	}
	if out, err := h.ReadFile(ctx, ReadRequest{Path: "b.txt"}); !errors.Is(err, fileread.ErrNotFound) {
		t.Fatalf("read of b.txt with no roots = %q, err = %v, want ErrNotFound", out.Result, err)
	}
	if _, err := h.ListDir(ctx, ListRequest{Path: "default.txt"}); !errors.Is(err, fileread.ErrNotDir) {
		t.Fatalf("listing the configured root's file with no roots err = %v, want ErrNotDir", err)
	}
}

// listTree builds the tree the listing-depth tests walk: a file at the top, one
// level down and two levels down.
//
//	<root>/top.txt
//	<root>/sub/inner.txt
//	<root>/sub/deeper/bottom.txt
func listTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range []struct{ path, content string }{
		{"top.txt", "top\n"},
		{"sub/inner.txt", "inner\n"},
		{"sub/deeper/bottom.txt", "bottom\n"},
	} {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The depth of a listing is the host's to validate and the plugin's to render.
// The host passes on a depth inside the range, reads a request that named none as
// the one level the tool has always been, and refuses a depth outside the range
// before the path is even resolved — the refusal is about the call, not about
// whether the directory happens to exist.
func TestListDirDepthReachesThePluginAndRefusesOutsideTheRange(t *testing.T) {
	root := listTree(t)
	h := testHost(t, Options{ReadRoot: root})
	ctx := context.Background()

	one, err := h.ListDir(ctx, ListRequest{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.Result, "top.txt") || !strings.Contains(one.Result, "sub") {
		t.Fatalf("the one-level listing lost its entries:\n%s", one.Result)
	}
	if strings.Contains(one.Result, "sub/inner.txt") {
		t.Fatalf("a request that named no depth entered a subdirectory:\n%s", one.Result)
	}
	// Zero is how the protocol says "no depth was named", so it is the default
	// of one level and not a request for nothing.
	zero, err := h.ListDir(ctx, ListRequest{Path: ".", Depth: 0})
	if err != nil {
		t.Fatal(err)
	}
	if zero.Result != one.Result {
		t.Fatalf("depth 0 changed the listing:\n%q\n%q", zero.Result, one.Result)
	}

	two, err := h.ListDir(ctx, ListRequest{Path: ".", Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(two.Result, "sub/inner.txt") {
		t.Fatalf("depth 2 did not reach the second level:\n%s", two.Result)
	}
	if strings.Contains(two.Result, "sub/deeper/bottom.txt") {
		t.Fatalf("depth 2 went to the third level:\n%s", two.Result)
	}
	// The deepest depth the listing has is enough for this tree, and the paths
	// stay relative to the directory the call named.
	deep, err := h.ListDir(ctx, ListRequest{Path: ".", Depth: fileread.MaxListDepth})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deep.Result, "sub/deeper/bottom.txt") {
		t.Fatalf("the deepest listing did not reach the third level:\n%s", deep.Result)
	}
	inside, err := h.ListDir(ctx, ListRequest{Path: "sub", Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inside.Result, "deeper/bottom.txt") {
		t.Fatalf("the paths must be relative to the directory listed:\n%s", inside.Result)
	}

	// A depth outside the range is refused here, with the range it may ask for,
	// and no plugin call is made for it.
	for _, depth := range []int{-1, -100, fileread.MaxListDepth + 1, 100} {
		out, err := h.ListDir(ctx, ListRequest{Path: ".", Depth: depth})
		if !errors.Is(err, fileread.ErrDepthInvalid) {
			t.Fatalf("depth %d: result %q, error %v, want ErrDepthInvalid", depth, out.Result, err)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%d..%d", fileread.DefaultListDepth, fileread.MaxListDepth)) {
			t.Fatalf("the refusal must name the range: %v", err)
		}
	}
	// The depth is refused whether or not the path exists: the host checks the
	// depth before resolving anything.
	if _, err := h.ListDir(ctx, ListRequest{Path: "absent", Depth: fileread.MaxListDepth + 1}); !errors.Is(err, fileread.ErrDepthInvalid) {
		t.Fatalf("a bad depth next to a missing path = %v, want ErrDepthInvalid", err)
	}
	if _, err := h.ListDir(ctx, ListRequest{Path: "/etc", Depth: 9}); !errors.Is(err, fileread.ErrDepthInvalid) {
		t.Fatalf("a bad depth next to an absolute path = %v, want ErrDepthInvalid", err)
	}
}

// The scan cap is the host's, and the plugin only carries it: a listing that goes
// deeper than its depth stops at the cap the host sent and states which cap it
// was, so a prefix of the tree is never read as the whole tree.
func TestListDirStatesTheScanCapTheHostConfigured(t *testing.T) {
	root := listTree(t)
	h := testHost(t, Options{ReadRoot: root, ListMaxScanned: 2})
	out, err := h.ListDir(context.Background(), ListRequest{Path: ".", Depth: fileread.MaxListDepth})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Result, "the listing stopped after 2 entries were examined; the remaining entries were not examined") {
		t.Fatalf("the configured scan cap must be stated:\n%s", out.Result)
	}
	if strings.Contains(out.Result, "sub/deeper/bottom.txt") {
		t.Fatalf("entries past the scan cap were rendered anyway:\n%s", out.Result)
	}
	// One level never reaches the scan cap, however small it is: a single
	// directory read is bounded by how many entries it renders.
	one, err := h.ListDir(context.Background(), ListRequest{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.Result, "top.txt") || strings.Contains(one.Result, "stopped") {
		t.Fatalf("the scan cap must not cut a one-level listing short:\n%s", one.Result)
	}
}

// findTree builds the tree the name-search tests walk: two test files at
// different depths, a name that merely starts with another name, and a symbolic
// link whose target is outside the root.
func findTree(t *testing.T) (root, outside string) {
	t.Helper()
	root = t.TempDir()
	outside = t.TempDir()
	for path, content := range map[string]string{
		"main.go":                 "package main\n",
		"main.go.bak":             "package main\n",
		"engine/engine.go":        "package engine\n",
		"engine/engine_test.go":   "package engine\n",
		"docs/deep/deep_test.go":  "package deep\n",
		"docs/deep/deep_bench.go": "package deep\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret_test.go"), []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret_test.go"), filepath.Join(root, "link-outside")); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

// The name-search tool reaches every depth under the root it is given, matches
// the whole name rather than a substring of it, and is validated on the host
// side before any RPC — the plugin never sees a path no root holds.
func TestFindFilesWalksTheRootAndRefusesOnTheHostSide(t *testing.T) {
	readRoot, outside := findTree(t)
	h := testHost(t, Options{ReadRoot: readRoot})
	ctx := context.Background()

	out, err := h.FindFiles(ctx, FindRequest{Path: ".", Pattern: "*_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docs/deep/deep_test.go", "engine/engine_test.go"} {
		if !strings.Contains(out.Result, want) {
			t.Fatalf("find did not reach %s:\n%s", want, out.Result)
		}
	}
	if strings.Contains(out.Result, "secret_test.go") || strings.Contains(out.Result, outside) {
		t.Fatalf("the walk followed a symbolic link out of the root:\n%s", out.Result)
	}
	if !strings.Contains(out.Result, "symbolic link") {
		t.Fatalf("the result does not say a link was left alone:\n%s", out.Result)
	}
	if out.Version != "1.0.0" || out.Generation == 0 || out.PluginPID <= 0 {
		t.Fatalf("metadata lost: %+v", out)
	}

	// The pattern is anchored to the whole name: a name that merely starts with
	// another name is not a match, and a matched directory is marked as one.
	anchored, err := h.FindFiles(ctx, FindRequest{Path: ".", Pattern: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(anchored.Result, "main.go") || strings.Contains(anchored.Result, "main.go.bak") {
		t.Fatalf("the pattern was not anchored to the whole name:\n%s", anchored.Result)
	}
	directory, err := h.FindFiles(ctx, FindRequest{Path: ".", Pattern: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(directory.Result, "docs/deep/") {
		t.Fatalf("a matched directory is not marked:\n%s", directory.Result)
	}

	// A refusal the host makes is a sentinel, and it is made before any plugin
	// runs: an empty or malformed pattern, a pattern aimed at a path, an
	// absolute path, an escape, a missing path and a symbolic link as the
	// starting point.
	cases := []struct {
		name    string
		request FindRequest
		want    error
	}{
		{"empty pattern", FindRequest{Path: ".", Pattern: ""}, fileread.ErrPatternEmpty},
		{"malformed pattern", FindRequest{Path: ".", Pattern: "["}, fileread.ErrPatternInvalid},
		{"pattern with a separator", FindRequest{Path: ".", Pattern: "docs/*.go"}, fileread.ErrPatternInvalid},
		{"absolute path", FindRequest{Path: "/etc", Pattern: "*"}, fileread.ErrPathAbsolute},
		{"escape", FindRequest{Path: "../" + filepath.Base(outside), Pattern: "*"}, fileread.ErrPathEscape},
		{"missing path", FindRequest{Path: "absent", Pattern: "*"}, fileread.ErrNotFound},
		// The host resolves the path before any plugin sees it, so a link is
		// refused by the containment check rather than by the plugin's own
		// "a find does not start at a link" rule, which a caller that hands
		// over an unresolved path still gets.
		{"symlink start", FindRequest{Path: "link-outside", Pattern: "*"}, fileread.ErrSymlinkEscape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := h.FindFiles(ctx, tc.request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("FindFiles(%+v) error = %v, want %v", tc.request, err, tc.want)
			}
			if out.Result != "" {
				t.Fatalf("a refused find returned a result: %q", out.Result)
			}
			if strings.Contains(err.Error(), readRoot) || strings.Contains(err.Error(), outside) {
				t.Fatalf("error leaks an absolute host path: %v", err)
			}
		})
	}
}

// A find that stopped says so: the path cap bounds how many matches are
// rendered, and the result states that the rest of the tree was not examined
// rather than reading as a complete answer.
func TestFindFilesStatesThePathCapItReached(t *testing.T) {
	readRoot, _ := findTree(t)
	h := testHost(t, Options{ReadRoot: readRoot, FindMaxPaths: 1})
	out, err := h.FindFiles(context.Background(), FindRequest{Path: ".", Pattern: "*_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Result, "1 path matches") {
		t.Fatalf("the cap did not bound the rendered paths:\n%s", out.Result)
	}
	if !strings.Contains(out.Result, "stopped") || !strings.Contains(out.Result, "not examined") {
		t.Fatalf("the result does not state the cap it reached:\n%s", out.Result)
	}
}

// A name search survives replacing its candidate, and the replacement is
// observable in exactly one column: the size unit.
func TestFindFilesSurvivesReplacementAndV2ChangesOnlyTheSizes(t *testing.T) {
	readRoot, _ := findTree(t)
	if err := os.WriteFile(filepath.Join(readRoot, "engine", "large.go"), []byte(strings.Repeat("a", 2048)), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, Options{ReadRoot: readRoot})
	before, err := h.FindFiles(context.Background(), FindRequest{Path: ".", Pattern: "large.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before.Result, "2.0 KiB") {
		t.Fatalf("v1 must render a human-readable size:\n%s", before.Result)
	}
	if err := reloadFixture(t, h, "updated"); err != nil {
		t.Fatal(err)
	}
	after, err := h.FindFiles(context.Background(), FindRequest{Path: ".", Pattern: "large.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.Result, "2048 B") {
		t.Fatalf("v2 must render an exact byte count:\n%s", after.Result)
	}
	if after.Generation == before.Generation || after.PluginPID == before.PluginPID {
		t.Fatalf("find replacement did not create a new generation: before=%+v after=%+v", before, after)
	}
	for _, result := range []string{before.Result, after.Result} {
		if !strings.Contains(result, "engine/large.go") {
			t.Fatalf("the matched path changed with the candidate: %q", result)
		}
	}
}

func TestReadFilePinsInflightCallAndDrainsOnReload(t *testing.T) {
	readRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(readRoot, "notes.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := slowCallOptions()
	opts.ReadRoot = readRoot
	h := testHost(t, opts)
	before := *active(t, h, ToolReadFile)

	type reply struct {
		out Output
		err error
	}
	done := make(chan reply, 1)
	go func() {
		// The same window as the replacement test: the call has to still be
		// running when the reload publishes the next generation.
		out, err := h.ReadFile(context.Background(), ReadRequest{Path: "notes.txt", DelayMS: 10000})
		done <- reply{out, err}
	}()
	waitFor(t, func() bool { a := h.State().Active(ToolReadFile); return a != nil && a.Inflight == 1 })

	if err := reloadFixture(t, h, "updated"); err != nil {
		t.Fatal(err)
	}
	current := active(t, h, ToolReadFile)
	if current.Version != "2.0.0" || current.PluginPID == before.PluginPID || current.Generation == before.Generation {
		t.Fatalf("reader not replaced: before=%+v after=%+v", before, current)
	}
	// The pinned reader generation drains only after its call returns.
	if current.Inflight != 0 {
		t.Fatalf("new generation carries the old inflight count: %+v", current)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.out.Result != "content\n" || got.out.Generation != before.Generation || got.out.PluginPID != before.PluginPID {
		t.Fatalf("reader lost its pin: %+v", got.out)
	}
	waitFor(t, func() bool {
		return len(h.State().Plugins) == len(Allowlist) && syscall.Kill(before.PluginPID, 0) == syscall.ESRCH
	})
}
