package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// newRememberStore is a real store in a temporary directory: the tool is
// exercised against the durable implementation, not a stub.
func newRememberStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), ".runtime", "memory.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

// blockedStore is a store whose file path is a directory, so every write fails
// the way an unwritable state file would — a real failure, not a fake.
func blockedStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".runtime", "memory.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return store
}

// runCtx carries the run identity the Kernel hands a tool call.
func runCtx(sessionID string) context.Context {
	return plugin.WithRun(context.Background(), plugin.RunInfo{RunID: "run-1", SessionID: sessionID})
}

func TestRememberToolAppendsOneFactAndCarriesNoPluginIdentity(t *testing.T) {
	store := newRememberStore(t)
	got, err := NewRememberTool(store).Invoke(runCtx("session-7"), `{"text":"prefers short answers"}`)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if got != rememberConfirmation {
		t.Fatalf("model-visible result=%q", got)
	}
	facts := mustRead(t, store)
	if len(facts) != 1 {
		t.Fatalf("facts=%+v", facts)
	}
	fact := facts[0]
	if fact.Type != TypeFact || fact.Text != "prefers short answers" || fact.SourceSession != "session-7" {
		t.Fatalf("fact=%+v", fact)
	}
	if fact.At.IsZero() {
		t.Fatal("the fact carries no timestamp")
	}
	// The model-visible answer names no plugin identity and echoes no text the
	// model did not supply itself.
	for _, leak := range []string{"generation", "version", "plugin_pid", "pid"} {
		if strings.Contains(strings.ToLower(got), leak) {
			t.Fatalf("model-visible output leaked identity %q: %q", leak, got)
		}
	}
}

func TestRememberToolRejectsEmptyAndOversizedText(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
		want      string
	}{
		{"missing text", `{}`, "text is required"},
		{"empty text", `{"text":""}`, "text is required"},
		{"whitespace only text", `{"text":"   \n "}`, "text is required"},
		{"over the character cap", `{"text":"` + strings.Repeat("x", MaxFactChars+1) + `"}`, "text is longer than"},
		{"multi-byte over the character cap", `{"text":"` + strings.Repeat("語", MaxFactChars+1) + `"}`, "text is longer than"},
		{"trailing JSON", `{"text":"a"}{"text":"b"}`, ""},
		{"unknown field", `{"text":"a","session":"b"}`, ""},
		{"not JSON", `remember this please`, ""},
		{"empty arguments", ``, ""},
	}
	for _, tc := range cases {
		store := newRememberStore(t)
		_, err := NewRememberTool(store).Invoke(runCtx("session-1"), tc.arguments)
		if err == nil {
			t.Fatalf("%s: accepted %q", tc.name, tc.arguments)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%q, want it to name %q", tc.name, err, tc.want)
		}
		// An input the model got wrong is a call-level refusal: the model can
		// correct it, so the round must not end over it.
		if plugin.IsUnavailable(err) {
			t.Fatalf("%s: an input refusal is marked as an infrastructure failure: %v", tc.name, err)
		}
		if facts := mustRead(t, store); len(facts) != 0 {
			t.Fatalf("%s: a refused call reached the store: %+v", tc.name, facts)
		}
	}
}

// The memory tool has exactly one write action and one parameter.
func TestRememberSchemaIsStrictAndWriteOnly(t *testing.T) {
	tool := NewRememberTool(nil)
	if tool.Name() != RememberToolName {
		t.Fatalf("tool name=%q", tool.Name())
	}
	encoded, err := json.Marshal(tool.Schema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["type"] != "object" || raw["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %s", encoded)
	}
	required, ok := raw["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "text" {
		t.Fatalf("required mismatch: %s", encoded)
	}
	properties, ok := raw["properties"].(map[string]any)
	if !ok || len(properties) != 1 {
		t.Fatalf("the tool exposes more than one parameter: %s", encoded)
	}
	if _, ok := properties["text"]; !ok {
		t.Fatalf("properties=%v", properties)
	}
	// The parameter description is part of the contract the model reads.
	text, _ := properties["text"].(map[string]any)
	if text["description"] != "One durable fact about the user, stated in a single sentence" {
		t.Fatalf("the text parameter description changed: %v", text["description"])
	}
	// No action switch: an action parameter is how a read or a delete would
	// reach the model, and the tool has neither.
	lowered := strings.ToLower(tool.Description())
	if strings.Contains(lowered, "read back") && !strings.Contains(lowered, "cannot be read back") {
		t.Fatalf("the description suggests a read path: %q", tool.Description())
	}
}

// 模型只能追加事实；用户能从页头的独立记忆入口撤回。这条指引现在写在工具自己的
// Description 里——系统指令不再由插件维护。
func TestTheMemoryCopySaysTheUserCanRetractAFact(t *testing.T) {
	text := NewRememberTool(nil).Description()
	if !strings.Contains(text, "retract") {
		t.Fatalf("the memory copy must say that the user can retract a stored fact: %q", text)
	}
	if !strings.Contains(text, "Memory (记忆) panel") || !strings.Contains(text, "page header") {
		t.Error("the memory copy must point to the dedicated Memory (记忆) panel opened from the page header")
	}
	if strings.Contains(strings.ToLower(text), "runtime drawer") {
		t.Error("the memory copy must not direct the user to the runtime drawer")
	}
}

func TestRememberToolFailsLoudlyWithoutAStore(t *testing.T) {
	if _, err := NewRememberTool(nil).Invoke(runCtx("session-1"), `{"text":"a fact"}`); err == nil {
		t.Fatal("expected an error when memory is not configured")
	}
}

// A store that cannot be written is an infrastructure failure: the round ends
// rather than the model being told that this one call was refused.
func TestRememberToolReportsAStoreFailure(t *testing.T) {
	store := blockedStore(t)
	_, err := NewRememberTool(store).Invoke(runCtx("session-1"), `{"text":"a fact"}`)
	if err == nil {
		t.Fatal("a write that could not land was reported as success")
	}
	if !plugin.IsUnavailable(err) || !errors.Is(err, plugin.ErrUnavailable) {
		t.Fatalf("a store failure must be marked as an infrastructure failure: %v", err)
	}
	if !strings.Contains(err.Error(), "store the fact") {
		t.Fatalf("the error must name the failed step: %v", err)
	}
	// Nothing landed: the blocked path is still the directory it was, so no file
	// was written in its place.
	info, statErr := os.Stat(store.path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("the blocked write replaced the path: %v", statErr)
	}
}

// The stored fact never carries plugin identity, whatever the tool reported.
func TestStoredFactsCarryNoPluginIdentity(t *testing.T) {
	store := newRememberStore(t)
	if _, err := store.Remember("session-1", "writes Go", time.Unix(0, 0)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := NewRememberTool(store).Invoke(runCtx("session-1"), `{"text":"prefers short answers"}`); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	facts := mustRead(t, store)
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"generation", "plugin_pid", "version", "pid"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("a stored fact carries %q: %s", forbidden, encoded)
		}
	}
	for _, fact := range facts {
		if fact.SourceSession != "session-1" {
			t.Fatalf("fact=%+v", fact)
		}
	}
}

// A run that carries no identity stores the fact with an empty source session:
// no attribution is honest, and a made-up id would not be.
func TestRememberWithoutRunIdentityStoresAnEmptySource(t *testing.T) {
	store := newRememberStore(t)
	if _, err := NewRememberTool(store).Invoke(context.Background(), `{"text":"a fact"}`); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	facts := mustRead(t, store)
	if len(facts) != 1 || facts[0].SourceSession != "" {
		t.Fatalf("facts=%+v", facts)
	}
}

func TestTheToolIsBoundToThePluginDescriptor(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), ".runtime", "memory.jsonl"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tools := p.Tools()
	if len(tools) != 1 || tools[0].Name() != RememberToolName {
		t.Fatalf("the plugin must expose exactly %q: %v", RememberToolName, tools)
	}
}
