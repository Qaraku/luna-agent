package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/memory"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
)

// memoryFactJSON is one fact as the browser sees it. The stored record type is
// not part of the browser contract: every element of facts is a fact.
type memoryFactJSON struct {
	Text          string    `json:"text"`
	At            time.Time `json:"at"`
	SourceSession string    `json:"source_session"`
}

type memoryRetractedJSON struct {
	Text        string    `json:"text"`
	At          time.Time `json:"at"`
	RetractedAt time.Time `json:"retracted_at"`
}

type memoryViewJSON struct {
	Facts     []memoryFactJSON      `json:"facts"`
	Retracted []memoryRetractedJSON `json:"retracted"`
}

// handlerWithMemory wires a real memory store, so the endpoint is exercised
// through its real implementation rather than a stub.
func handlerWithMemory(t *testing.T) (http.Handler, *memory.Store) {
	t.Helper()
	store, err := memory.Open(filepath.Join(t.TempDir(), ".runtime", "memory.jsonl"))
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithMemory(store)), store
}

func decodeMemoryView(t *testing.T, w *httptest.ResponseRecorder) memoryViewJSON {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var view memoryViewJSON
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode memory view: %v (body=%s)", err, w.Body.String())
	}
	return view
}

func TestMemoryEndpointListsWhatIsStored(t *testing.T) {
	h, store := handlerWithMemory(t)
	at := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.Remember("session-a", "prefers Go", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := store.Remember("session-b", "uses voice input", at.Add(time.Minute)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	view := decodeMemoryView(t, request(t, h, http.MethodGet, "/api/memory", "", false))
	if len(view.Facts) != 2 || view.Facts[0].Text != "prefers Go" || view.Facts[1].Text != "uses voice input" {
		t.Fatalf("facts=%+v", view.Facts)
	}
	if view.Facts[0].SourceSession != "session-a" || !view.Facts[0].At.Equal(at) {
		t.Fatalf("a fact must carry where and when it came from: %+v", view.Facts[0])
	}
	if len(view.Retracted) != 0 {
		t.Fatalf("retracted=%+v", view.Retracted)
	}
	// The stored record type is not part of this contract.
	if strings.Contains(w0Body(t, h), `"type"`) {
		t.Fatalf("the memory view leaks the storage record type: %s", w0Body(t, h))
	}

	// A retracted fact leaves the list and appears as retracted.
	if _, err := store.Retract(at, "prefers Go"); err != nil {
		t.Fatalf("Retract: %v", err)
	}
	view = decodeMemoryView(t, request(t, h, http.MethodGet, "/api/memory", "", false))
	if len(view.Facts) != 1 || view.Facts[0].Text != "uses voice input" {
		t.Fatalf("facts after a retraction=%+v", view.Facts)
	}
	if len(view.Retracted) != 1 || view.Retracted[0].Text != "prefers Go" || view.Retracted[0].RetractedAt.IsZero() {
		t.Fatalf("retracted=%+v", view.Retracted)
	}
}

// w0Body is the body of a fresh GET, for assertions that need the raw JSON.
func w0Body(t *testing.T, h http.Handler) string {
	t.Helper()
	return request(t, h, http.MethodGet, "/api/memory", "", false).Body.String()
}

func TestMemoryEndpointIsEmptyForAFreshStore(t *testing.T) {
	h, _ := handlerWithMemory(t)
	view := decodeMemoryView(t, request(t, h, http.MethodGet, "/api/memory", "", false))
	if len(view.Facts) != 0 || len(view.Retracted) != 0 {
		t.Fatalf("fresh memory=%+v", view)
	}
	if body := w0Body(t, h); strings.Contains(body, "null") {
		t.Fatalf("an empty memory must be an empty list, not null: %s", body)
	}
}

func TestMemoryEndpointRejectsMethodsOtherThanGet(t *testing.T) {
	h, _ := handlerWithMemory(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		if w := request(t, h, method, "/api/memory", "", true); w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/memory status=%d, want 405", method, w.Code)
		}
	}
}

func TestRetractEndpointRemovesOneFact(t *testing.T) {
	h, store := handlerWithMemory(t)
	at := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.Remember("session-a", "keep me", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := store.Remember("session-a", "drop me", at.Add(time.Minute)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	body := `{"at":"` + at.Add(time.Minute).Format(time.RFC3339Nano) + `","text":"drop me"}`
	w := request(t, h, http.MethodPost, "/api/memory/retract", body, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Retracted memoryRetractedJSON `json:"retracted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if response.Retracted.Text != "drop me" || response.Retracted.RetractedAt.IsZero() {
		t.Fatalf("retracted=%+v", response.Retracted)
	}
	view := decodeMemoryView(t, request(t, h, http.MethodGet, "/api/memory", "", false))
	if len(view.Facts) != 1 || view.Facts[0].Text != "keep me" {
		t.Fatalf("facts after retracting=%+v", view.Facts)
	}
}

func TestRetractEndpointRefusesAFactThatIsNotInEffect(t *testing.T) {
	h, store := handlerWithMemory(t)
	at := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.Remember("s", "the only fact", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	body := `{"at":"` + at.Format(time.RFC3339Nano) + `","text":"a different fact"}`
	if w := request(t, h, http.MethodPost, "/api/memory/retract", body, true); w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (body=%s)", w.Code, w.Body.String())
	}
	// Retracting twice is the same case: the fact is no longer in effect.
	good := `{"at":"` + at.Format(time.RFC3339Nano) + `","text":"the only fact"}`
	if w := request(t, h, http.MethodPost, "/api/memory/retract", good, true); w.Code != http.StatusOK {
		t.Fatalf("first retraction status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodPost, "/api/memory/retract", good, true); w.Code != http.StatusNotFound {
		t.Fatalf("second retraction status=%d, want 404", w.Code)
	}
}

func TestRetractEndpointRejectsMalformedRequests(t *testing.T) {
	h, _ := handlerWithMemory(t)
	cases := map[string]string{
		"no text":       `{"at":"2026-09-25T10:00:00Z"}`,
		"empty text":    `{"at":"2026-09-25T10:00:00Z","text":""}`,
		"no time":       `{"text":"something"}`,
		"bad time":      `{"at":"yesterday","text":"something"}`,
		"unknown field": `{"at":"2026-09-25T10:00:00Z","text":"x","index":3}`,
		"trailing json": `{"at":"2026-09-25T10:00:00Z","text":"x"}{"at":"2026-09-25T10:00:00Z"}`,
		"not json":      `nonsense`,
		"empty body":    ``,
	}
	for name, body := range cases {
		if w := request(t, h, http.MethodPost, "/api/memory/retract", body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400 (body=%s)", name, w.Code, w.Body.String())
		}
	}
	// A retraction is a mutation: it needs the exact bound Origin, and the guard
	// runs before the method check, so a wrong-origin GET is refused as an
	// origin problem rather than reported as a method problem.
	good := `{"at":"2026-09-25T10:00:00Z","text":"x"}`
	if w := request(t, h, http.MethodPost, "/api/memory/retract", good, false); w.Code != http.StatusForbidden {
		t.Fatalf("no origin: status=%d, want 403", w.Code)
	}
	if w := request(t, h, http.MethodGet, "/api/memory/retract", "", true); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET retract with origin: status=%d, want 405", w.Code)
	}
}

func TestMemoryEndpointReportsACorruptStoreWithoutAHostPath(t *testing.T) {
	h, _ := handlerWithMemory(t)
	// A store opened on a file with a malformed complete line must report the
	// corruption rather than showing an empty memory.
	dir := filepath.Join(t.TempDir(), ".runtime")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "memory.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"fact\",\"text\":\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePlugins{state: pluginState(pluginhost.ToolTextTransform, pluginhost.ToolReadFile)}
	broken := New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, WithMemory(store))
	if w := request(t, broken, http.MethodGet, "/api/memory", "", false); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	} else if strings.Contains(w.Body.String(), dir) {
		t.Fatalf("the error leaks a host path: %s", w.Body.String())
	}
	_ = h
}

func TestMemoryEndpointWithoutAStore(t *testing.T) {
	h := testHandler(t, fakeRunner{})
	if w := request(t, h, http.MethodGet, "/api/memory", "", false); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
	if w := request(t, h, http.MethodPost, "/api/memory/retract", `{"at":"2026-09-25T10:00:00Z","text":"x"}`, true); w.Code != http.StatusInternalServerError {
		t.Fatalf("retract without a store: status=%d, want 500", w.Code)
	}
}
