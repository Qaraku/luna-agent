package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReloadTargetsARegisteredToolNotAVersion(t *testing.T) {
	h := testHost(t, Options{})
	before := h.State()
	if err := h.Reload(context.Background(), ToolReadFile); err != nil {
		t.Fatalf("reload registered source: %v", err)
	}
	after := h.State()
	for _, spec := range Allowlist {
		old, now := before.Active(spec.Tool), after.Active(spec.Tool)
		if spec.Tool == ToolReadFile {
			if old.PluginPID == now.PluginPID || old.Generation == now.Generation {
				t.Fatal("selected tool was not replaced")
			}
		} else if old.PluginPID != now.PluginPID || old.Generation != now.Generation {
			t.Fatalf("unselected tool %s was restarted", spec.Tool)
		}
	}
	raw, err := json.Marshal(after.Plugins)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, present := row["candidate"]; present {
			t.Fatal("runtime state still exposes a demonstration candidate")
		}
	}
}

func TestReloadRejectsLegacyVersionTargets(t *testing.T) {
	h := testHost(t, Options{})
	for _, name := range []string{"v1", "v2", "broken", "../../bin/sh"} {
		if err := h.Reload(context.Background(), name); !errors.Is(err, ErrUnknownTool) {
			t.Errorf("target %q: error=%v, want unknown tool", name, err)
		}
	}
}

func TestRegisteredToolsHaveDirectProductionSources(t *testing.T) {
	for _, spec := range Allowlist {
		if _, err := os.Stat(filepath.Join(testRoot(t), "plugins", spec.Dir, "main.go")); err != nil {
			t.Errorf("%s has no direct implementation: %v", spec.Tool, err)
		}
	}
}

func TestReloadAllKeepsEveryOldProcessWhenALaterSourceFails(t *testing.T) {
	h := testHost(t, Options{})
	prepareBuildRoot(t, h)
	before := h.State()
	last := Allowlist[len(Allowlist)-1]
	path := filepath.Join(h.root, "plugins", last.Dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\nthis cannot compile\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(context.Background(), ""); err == nil {
		t.Fatal("invalid source was published")
	}
	after := h.State()
	for _, spec := range Allowlist {
		old, now := before.Active(spec.Tool), after.Active(spec.Tool)
		if old.PluginPID != now.PluginPID || old.Generation != now.Generation {
			t.Fatalf("partial replacement of %s", spec.Tool)
		}
	}
	entries, err := os.ReadDir(filepath.Join(h.root, ".runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed reload left %d replacement binaries", len(entries))
	}
}

func TestReloadAcceptsPluginReportedVersionsWithoutAnEnumeration(t *testing.T) {
	h := testHost(t, Options{})
	prepareBuildRoot(t, h)
	path := filepath.Join(h.root, "plugins", "text_transform", "main.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), "1.0.0", "local-build-2026", 1)
	if changed == string(data) {
		t.Fatal("version fixture not changed")
	}
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(context.Background(), ToolTextTransform); err != nil {
		t.Fatal(err)
	}
	if got := h.State().Active(ToolTextTransform).Version; got != "local-build-2026" {
		t.Fatalf("reported version=%q", got)
	}
}

func TestRegistrationRejectsSourcesOutsideThePluginTree(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "plugins", "escape")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"../", "/bin", "escape"} {
		h, err := New(context.Background(), root, Options{Tools: []ToolSpec{{Tool: "safe_name", Dir: dir}}})
		if h != nil {
			h.Close()
		}
		if err == nil {
			t.Fatalf("source %q accepted", dir)
		}
	}
}

func TestMissingLazySourceDoesNotBlockHostStartup(t *testing.T) {
	root := t.TempDir()
	h, err := New(context.Background(), root, Options{Tools: []ToolSpec{{Tool: "optional_text", Dir: "missing", Lazy: true}}})
	if err != nil {
		t.Fatalf("optional source blocked startup: %v", err)
	}
	defer h.Close()
	if len(h.State().Plugins) != 0 || len(h.State().Tools) != 1 {
		t.Fatalf("state=%+v", h.State())
	}
	if _, err := h.InvokeText(context.Background(), "optional_text", "{}", ""); !errors.Is(err, ErrNoActivePlugin) {
		t.Fatalf("missing process error=%v", err)
	}
}
