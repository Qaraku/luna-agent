package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Qaraku/luna-agent/internal/command"
)

// commandsHandler builds a handler with the given command table. It uses the
// same plugin and runner doubles as the other interface tests, because what is
// under test here is the command contract and nothing else.
func commandsHandler(t *testing.T, commands ...command.Command) http.Handler {
	t.Helper()
	table, err := command.New(commands...)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePlugins{state: everyAllowlistedTool()}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithCommands(table))
}

type commandEntry struct {
	Name     string   `json:"name"`
	Summary  string   `json:"summary"`
	Usage    string   `json:"usage"`
	Category string   `json:"category"`
	Aliases  []string `json:"aliases"`
	Args     string   `json:"args"`
	Options  []struct {
		Value   string `json:"value"`
		Summary string `json:"summary"`
	} `json:"options"`
	Busy string `json:"busy"`
}

func decodeCommands(t *testing.T, w *httptest.ResponseRecorder) []commandEntry {
	t.Helper()
	var body struct {
		Commands []commandEntry `json:"commands"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return body.Commands
}

func entryNamed(entries []commandEntry, name string) *commandEntry {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}

// The table exists so the interface does not have to describe commands itself:
// what the server reports is what the composer draws, including the parts a user
// decides with — the summary, the argument shape, and what happens if a run is
// already active.
func TestCommandsAreServedFromTheTable(t *testing.T) {
	w := request(t, commandsHandler(t, command.Builtins()...), http.MethodGet, "/api/commands", "", false)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	help := entryNamed(decodeCommands(t, w), "help")
	if help == nil {
		t.Fatalf("help is missing from %s", w.Body.String())
	}
	if help.Summary == "" || help.Usage != "/help" || help.Args != "none" || help.Busy == "" {
		t.Fatalf("help was not described completely: %+v", help)
	}
}

// A command with a fixed set of values carries them, so the interface can render
// a picker instead of guessing what a user may type.
func TestACommandWithOptionsCarriesThem(t *testing.T) {
	w := request(t, commandsHandler(t, command.Command{
		Name:     "model",
		Summary:  "选择模型",
		Usage:    "/model [name]",
		Category: "会话",
		Aliases:  []string{"m"},
		Args:     command.ArgOptions,
		Options: []command.Option{
			{Value: "one", Summary: "第一个"},
			{Value: "two"},
		},
		Busy: command.BusyReject,
	}), http.MethodGet, "/api/commands", "", false)
	entries := decodeCommands(t, w)
	if len(entries) != 1 {
		t.Fatalf("commands = %+v", entries)
	}
	got := entries[0]
	if got.Args != "options" || got.Busy != "reject" || len(got.Options) != 2 {
		t.Fatalf("command = %+v", got)
	}
	if got.Options[0].Value != "one" || got.Options[0].Summary != "第一个" || got.Options[1].Value != "two" {
		t.Fatalf("options = %+v", got.Options)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "m" {
		t.Fatalf("aliases = %+v", got.Aliases)
	}
}

// A server with no table reports an empty list rather than a missing field: a
// browser should not have to tell "no commands" apart from "nothing was sent".
func TestNoCommandsIsAnEmptyList(t *testing.T) {
	p := &fakePlugins{state: everyAllowlistedTool()}
	handler := New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"})
	w := request(t, handler, http.MethodGet, "/api/commands", "", false)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var raw struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Commands == nil || len(raw.Commands) != 0 {
		t.Fatalf("commands = %s, want an empty list", w.Body.String())
	}
}

// Reading the table is a read: any other method is refused with the method that
// would have worked.
func TestTheCommandsEndpointIsReadOnly(t *testing.T) {
	w := request(t, commandsHandler(t, command.Builtins()...), http.MethodPost, "/api/commands", "{}", true)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q", allow)
	}
}
