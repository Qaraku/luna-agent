package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Qaraku/luna-agent/internal/command"
	"github.com/Qaraku/luna-agent/internal/store"
)

func controlsRequest(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return request(t, h, http.MethodPost, path, string(raw), true)
}

func TestCreateSessionBeforeFirstMessage(t *testing.T) {
	h, sessions := modelsHandler(t)
	made := controlsRequest(t, h, "/api/sessions", map[string]any{})
	if made.Code != http.StatusCreated {
		t.Fatalf("create status=%d: %s", made.Code, made.Body)
	}
	var created struct {
		ID       string
		RunCount int
	}
	if err := json.Unmarshal(made.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateID(created.ID); err != nil {
		t.Fatal(err)
	}
	selected := controlsRequest(t, h, "/api/sessions/"+created.ID+"/model", map[string]any{"model": "two"})
	if selected.Code != 200 {
		t.Fatalf("select before first message=%d: %s", selected.Code, selected.Body)
	}
	session, err := sessions.Read(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.RunCount != 0 || session.Config == nil || session.Config.Model != "two" {
		t.Fatalf("session=%+v", session)
	}
	for _, record := range session.Records {
		if record.Message != nil {
			t.Fatal("configuration created a conversation message")
		}
	}
}

func TestReasoningChoicesDistinguishOverrideOffAndDefault(t *testing.T) {
	sessions := newTestStore(t)
	h := New(&fakePlugins{state: everyAllowlistedTool()}, fakeRunner{}, sessions, Info{BoundHost: "127.0.0.1:43210", Model: "one", ReasoningEffort: "medium", Models: []ModelRef{{Name: "one"}, {Name: "two"}}})
	id, err := sessions.Create("reasoning")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		body           map[string]any
		effort, origin string
	}{
		{map[string]any{"reasoning_effort": "high"}, "high", "session"},
		{map[string]any{"reasoning_effort": "xhigh"}, "xhigh", "session"},
		{map[string]any{"reasoning_effort": "max"}, "max", "session"},
		{map[string]any{"reasoning_effort": ""}, "", "session"},
		{map[string]any{"reasoning_effort": "none"}, "none", "session"},
		{map[string]any{"reset": true}, "medium", "global"},
	}
	for _, tc := range cases {
		result := controlsRequest(t, h, "/api/sessions/"+id+"/reasoning", tc.body)
		if result.Code != 200 {
			t.Fatalf("set=%d: %s", result.Code, result.Body)
		}
		view := request(t, h, http.MethodGet, "/api/reasoning?session="+id, "", false)
		if view.Code != 200 {
			t.Fatalf("get=%d: %s", view.Code, view.Body)
		}
		var got struct {
			Current struct {
				Effort string `json:"reasoning_effort"`
				Origin string
			}
			Levels []string
		}
		if err := json.Unmarshal(view.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Current.Effort != tc.effort || got.Current.Origin != tc.origin || len(got.Levels) == 0 {
			t.Fatalf("view=%s", view.Body)
		}
	}
}

func TestReasoningRejectsInvalidChoicesWithoutWriting(t *testing.T) {
	h, sessions := modelsHandler(t)
	id, err := sessions.Create("invalid")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{{}, {"reasoning_effort": nil}, {"reasoning_effort": "maximum"}, {"reasoning_effort": "high", "reset": true}, {"reset": false}, {"reasoning_effort": 3}, {"unexpected": true}} {
		before, err := sessions.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		result := controlsRequest(t, h, "/api/sessions/"+id+"/reasoning", body)
		if result.Code != 400 {
			t.Errorf("body=%v status=%d want 400", body, result.Code)
		}
		after, err := sessions.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Records) != len(before.Records) {
			t.Fatal("invalid choice appended records")
		}
	}
	result := controlsRequest(t, h, "/api/sessions/0123456789abcdef/reasoning", map[string]any{"reasoning_effort": "high"})
	if result.Code != 404 {
		t.Fatalf("unknown session=%d", result.Code)
	}
}

func TestSessionModelCanResumeFollowingGlobalDefault(t *testing.T) {
	h, sessions := modelsHandler(t)
	id, err := sessions.Create("default")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/sessions/" + id + "/model"
	if got := controlsRequest(t, h, path, map[string]any{"model": "two"}); got.Code != 200 {
		t.Fatal(got.Body)
	}
	reset := controlsRequest(t, h, path, map[string]any{"reset": true})
	if reset.Code != 200 {
		t.Fatalf("reset=%d: %s", reset.Code, reset.Body)
	}
	got, err := sessions.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config == nil || got.Config.Model != "" {
		t.Fatalf("reset pins a model: %+v", got.Config)
	}
	view := decodeModels(t, request(t, h, http.MethodGet, "/api/models?session="+id, "", false).Body.Bytes())
	if view.Current.Name != "one" || view.Current.Origin != originGlobal {
		t.Fatalf("current=%+v", view.Current)
	}
	if got := controlsRequest(t, h, path, map[string]any{"model": "two", "reset": true}); got.Code != 400 {
		t.Fatalf("ambiguous reset=%d", got.Code)
	}
}

func TestBuiltinReasoningCommandCarriesDefaultAndOffChoices(t *testing.T) {
	h := commandsHandler(t, command.Builtins()...)
	entries := decodeCommands(t, request(t, h, http.MethodGet, "/api/commands", "", false))
	got := entryNamed(entries, "reasoning")
	if got == nil {
		t.Fatal("/reasoning is missing")
	}
	if got.Args != "options" || got.Busy != "reject" {
		t.Fatalf("command=%+v", got)
	}
	want := map[string]bool{"--default": false, "--off": false, "high": false, "none": false}
	for _, option := range got.Options {
		if _, ok := want[option.Value]; ok {
			want[option.Value] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing option %s", name)
		}
	}
}

func TestCreateSessionRequiresAnObjectAndNoUnknownFields(t *testing.T) {
	h, sessions := modelsHandler(t)
	for _, body := range []string{"null", "[]", "{} {}", "{\"unknown\":true}"} {
		got := request(t, h, http.MethodPost, "/api/sessions", body, true)
		if got.Code != 400 {
			t.Errorf("body %s status=%d want 400", body, got.Code)
		}
	}
	list, err := sessions.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("invalid input created %d sessions", len(list))
	}
}

func TestOtherSessionChoicesKeepReasoningSelection(t *testing.T) {
	sessions := newTestStore(t)
	h, workspaceID := sessionConfigHandler(t, sessions)
	id, err := sessions.Create("preserved")
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range []struct {
		field string
		body  map[string]any
	}{
		{"reasoning", map[string]any{"reasoning_effort": "high"}},
		{"model", map[string]any{"model": "two"}},
		{"workspace", map[string]any{"workspace": workspaceID}},
		{"model", map[string]any{"reset": true}},
	} {
		got := controlsRequest(t, h, "/api/sessions/"+id+"/"+choice.field, choice.body)
		if got.Code != 200 {
			t.Fatalf("choice=%s status=%d %s", choice.field, got.Code, got.Body)
		}
	}
	got, err := sessions.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config == nil || got.Config.ReasoningEffort == nil || *got.Config.ReasoningEffort != "high" || got.Config.Workspace != workspaceID || got.Config.Model != "" {
		t.Fatalf("config=%+v", got.Config)
	}
}
