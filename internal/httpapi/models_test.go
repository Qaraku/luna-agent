package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Qaraku/luna-agent/internal/store"
)

// modelsHandler builds a handler with two configured models, which is the
// smallest configuration in which choosing one means something.
func modelsHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	sessions := newTestStore(t)
	p := &fakePlugins{state: everyAllowlistedTool()}
	info := Info{
		BoundHost:    "127.0.0.1:43210",
		Model:        "one",
		ProviderHost: "provider.test",
		Models: []ModelRef{
			{Name: "one", Provider: "provider.test"},
			{Name: "two", Provider: "other.test"},
		},
		WebDir: "../../web",
	}
	return New(p, fakeRunner{}, sessions, info), sessions
}

type modelsBody struct {
	Models []struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Default  bool   `json:"default"`
	} `json:"models"`
	Current struct {
		Name   string `json:"name"`
		Origin string `json:"origin"`
	} `json:"current"`
}

func decodeModels(t *testing.T, body []byte) modelsBody {
	t.Helper()
	var decoded modelsBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return decoded
}

// Without a session the answer describes the configuration alone, and the first
// entry is marked as the default rather than left to be inferred from position.
func TestModelsListsTheConfiguredSetAndItsDefault(t *testing.T) {
	handler, _ := modelsHandler(t)
	w := request(t, handler, http.MethodGet, "/api/models", "", false)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := decodeModels(t, w.Body.Bytes())
	if len(body.Models) != 2 || body.Models[0].Name != "one" || body.Models[1].Name != "two" {
		t.Fatalf("models = %+v", body.Models)
	}
	if !body.Models[0].Default || body.Models[1].Default {
		t.Fatalf("default flag = %+v", body.Models)
	}
	if body.Current.Name != "one" || body.Current.Origin != originGlobal {
		t.Fatalf("current = %+v, want the default from the configuration", body.Current)
	}
}

// A session that chose a model says so, and says that it chose: a user coming
// back to a session needs to tell their own choice apart from the configuration's.
func TestASessionsOwnChoiceIsReportedWithItsOrigin(t *testing.T) {
	handler, sessions := modelsHandler(t)
	id, err := sessions.Create("chosen")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.AppendConfig(id, store.ConfigRecord{Type: store.TypeConfig, Model: "two"}); err != nil {
		t.Fatal(err)
	}
	w := request(t, handler, http.MethodGet, "/api/models?session="+id, "", false)
	body := decodeModels(t, w.Body.Bytes())
	if body.Current.Name != "two" || body.Current.Origin != originSession {
		t.Fatalf("current = %+v", body.Current)
	}
}

func TestAskingAboutAnUnknownSessionIsNotFound(t *testing.T) {
	handler, _ := modelsHandler(t)
	if w := request(t, handler, http.MethodGet, "/api/models?session=0123456789abcdef", "", false); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if w := request(t, handler, http.MethodGet, "/api/models?session=not%20an%20id", "", false); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// Choosing a model is a mutation: it needs the exact origin, it refuses a model
// this runtime does not have, and after it the session reports the choice.
func TestChoosingASessionModel(t *testing.T) {
	handler, sessions := modelsHandler(t)
	id, err := sessions.Create("chooser")
	if err != nil {
		t.Fatal(err)
	}

	w := request(t, handler, http.MethodPost, "/api/sessions/"+id+"/model", `{"model":"two"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var chosen map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &chosen); err != nil {
		t.Fatal(err)
	}
	if chosen["model"] != "two" || chosen["origin"] != originSession || chosen["session_id"] != id {
		t.Fatalf("response = %+v", chosen)
	}

	// The choice is recorded in the session itself, so a replay shows it.
	session, err := sessions.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if session.Config == nil || session.Config.Model != "two" {
		t.Fatalf("session config = %+v", session.Config)
	}
	body := decodeModels(t, request(t, handler, http.MethodGet, "/api/models?session="+id, "", false).Body.Bytes())
	if body.Current.Name != "two" || body.Current.Origin != originSession {
		t.Fatalf("current after choosing = %+v", body.Current)
	}
}

func TestChoosingAModelRefusesWhatCannotBeUsed(t *testing.T) {
	handler, sessions := modelsHandler(t)
	id, err := sessions.Create("chooser")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		target string
		body   string
		origin bool
		want   int
	}{
		{"unknown model", "/api/sessions/" + id + "/model", `{"model":"three"}`, true, http.StatusBadRequest},
		{"empty model", "/api/sessions/" + id + "/model", `{"model":"   "}`, true, http.StatusBadRequest},
		{"unknown session", "/api/sessions/0123456789abcdef/model", `{"model":"two"}`, true, http.StatusNotFound},
		{"malformed session", "/api/sessions/nope/model", `{"model":"two"}`, true, http.StatusBadRequest},
		{"no origin", "/api/sessions/" + id + "/model", `{"model":"two"}`, false, http.StatusForbidden},
		{"extra field", "/api/sessions/" + id + "/model", `{"model":"two","other":1}`, true, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, handler, http.MethodPost, tc.target, tc.body, tc.origin)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	// A refused switch must not have recorded anything: the session still uses
	// the configuration's default.
	session, err := sessions.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if session.Config != nil {
		t.Fatalf("a refused switch was recorded: %+v", session.Config)
	}
}

func TestSessionModelPathRecognizesOnlyTheModelEndpoint(t *testing.T) {
	id, ok := sessionModelPath("/api/sessions/abc/model")
	if !ok || id != "abc" {
		t.Fatalf("got %q, %v", id, ok)
	}
	for _, path := range []string{"/api/sessions/abc", "/api/sessions//model", "/api/sessions/abc/notmodel", "/api/models"} {
		if _, ok := sessionModelPath(path); ok {
			t.Fatalf("%q was accepted as the model endpoint", path)
		}
	}
}
