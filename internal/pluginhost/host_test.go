package pluginhost

import (
	"context"
	"errors"
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
		if record.Version != "v1" || record.Generation != 1 || record.Candidate != "v1" {
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
	// The allowlist itself stays inside the v1/v2/broken vocabulary, and every
	// candidate has a real build directory: a reload can never reach a path that
	// was not compiled from root source.
	vocabulary := map[string]bool{"v1": true, "v2": true, "broken": true}
	for _, spec := range Allowlist {
		if len(spec.Candidates) == 0 {
			t.Fatalf("%s has no candidates", spec.Tool)
		}
		for _, candidate := range spec.Candidates {
			if !vocabulary[candidate] {
				t.Fatalf("%s candidate %q is outside the v1/v2/broken vocabulary", spec.Tool, candidate)
			}
			dir := filepath.Join(testRoot(t), "plugins", spec.Dir, candidate)
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("%s candidate %q has no plugin directory at %s: %v", spec.Tool, candidate, dir, err)
			}
		}
	}
}

func TestRealSubprocessReplacementPinsInflightAndRollsBack(t *testing.T) {
	h := testHost(t, Options{})
	first := *active(t, h, ToolTextTransform)
	if first.Version != "v1" || first.PluginPID == os.Getpid() {
		t.Fatalf("bad v1 state: %+v", first)
	}
	if reader := active(t, h, ToolReadFile); reader.Version != "v1" {
		t.Fatalf("bad reader state: %+v", reader)
	}

	// Keep the old RPC in flight longer than a cached candidate build.
	done := make(chan Output, 1)
	errs := make(chan error, 1)
	go func() {
		out, err := h.Invoke(context.Background(), Input{Text: " first ", DelayMS: 3000})
		done <- out
		errs <- err
	}()
	waitFor(t, func() bool { a := h.State().Active(ToolTextTransform); return a != nil && a.Inflight == 1 })
	if err := h.Reload(context.Background(), "v2"); err != nil {
		t.Fatal(err)
	}
	second := *active(t, h, ToolTextTransform)
	if second.Version != "v2" || second.Generation == first.Generation || second.PluginPID == first.PluginPID {
		t.Fatalf("bad replacement: %+v", second)
	}
	// The pinned transform generation is retained; the reader's old generation
	// had nothing in flight and is already gone.
	if len(h.State().Plugins) != 3 {
		t.Fatalf("old generation not retained exactly once: %+v", h.State().Plugins)
	}
	if reader := active(t, h, ToolReadFile); reader.Version != "v2" || reader.Generation != second.Generation {
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
		return len(h.State().Plugins) == 2 && syscall.Kill(first.PluginPID, 0) == syscall.ESRCH
	})
	before := map[string]Record{}
	for _, tool := range []string{ToolTextTransform, ToolReadFile} {
		before[tool] = *active(t, h, tool)
	}
	if err := h.Reload(context.Background(), "broken"); err == nil {
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
	if err := h.Reload(context.Background(), "v1"); err != nil {
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

func TestRejectsUnknownCandidate(t *testing.T) {
	h := testHost(t, Options{})
	for _, candidate := range []string{"../../bin/sh", "read_file/v1", "/plugins/v1", "v3", ""} {
		if err := h.Reload(context.Background(), candidate); err == nil {
			t.Fatalf("unknown candidate %q accepted", candidate)
		}
	}
	if got := active(t, h, ToolTextTransform); got.Version != "v1" {
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
	if out.Version != "v1" || out.Generation != 1 || out.PluginPID <= 0 {
		t.Fatalf("metadata lost: %+v", out)
	}
	if transformer := active(t, h, ToolTextTransform); transformer.PluginPID == out.PluginPID {
		t.Fatal("reader and transformer share one process")
	}

	if err := h.Reload(context.Background(), "v2"); err != nil {
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
	if !strings.Contains(out.Result, "# Luna Agent architecture") {
		t.Fatalf("unexpected content: %.60q", out.Result)
	}
	narrowed := testHost(t, Options{ReadRoot: t.TempDir()})
	if _, err := narrowed.ReadFile(context.Background(), ReadRequest{Path: "docs/architecture.md"}); !errors.Is(err, fileread.ErrNotFound) {
		t.Fatalf("narrowed read root still reached the repository: %v", err)
	}
}

func TestReadFilePinsInflightCallAndDrainsOnReload(t *testing.T) {
	readRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(readRoot, "notes.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := testHost(t, Options{ReadRoot: readRoot})
	before := *active(t, h, ToolReadFile)

	type reply struct {
		out Output
		err error
	}
	done := make(chan reply, 1)
	go func() {
		out, err := h.ReadFile(context.Background(), ReadRequest{Path: "notes.txt", DelayMS: 3000})
		done <- reply{out, err}
	}()
	waitFor(t, func() bool { a := h.State().Active(ToolReadFile); return a != nil && a.Inflight == 1 })

	if err := h.Reload(context.Background(), "v2"); err != nil {
		t.Fatal(err)
	}
	current := active(t, h, ToolReadFile)
	if current.Version != "v2" || current.PluginPID == before.PluginPID || current.Generation == before.Generation {
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
