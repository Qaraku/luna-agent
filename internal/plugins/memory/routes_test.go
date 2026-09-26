package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// newRoutePlugin is a real plugin over a real store in a temporary directory,
// so the routes are exercised through their real implementation.
func newRoutePlugin(t *testing.T) (*Plugin, *Store) {
	t.Helper()
	p, err := New(filepath.Join(t.TempDir(), ".runtime", "memory.jsonl"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, p.store
}

// routeFor finds the plugin's route for a path. A missing route is a failure:
// the descriptor declares both paths.
func routeFor(t *testing.T, p *Plugin, path string) plugin.Route {
	t.Helper()
	for _, route := range p.Routes() {
		if route.Path() == path {
			return route
		}
	}
	t.Fatalf("the plugin exposes no route for %q", path)
	return nil
}

// serveRoute drives one route directly. Host, Origin and method matching are the
// Kernel's, so they are not set up here.
func serveRoute(t *testing.T, route plugin.Route, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:43210"+route.Path(), strings.NewReader(body))
	w := httptest.NewRecorder()
	route.ServeHTTP(w, r)
	return w
}

func decodeMemoryView(t *testing.T, w *httptest.ResponseRecorder) memoryView {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var view memoryView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode memory view: %v (body=%s)", err, w.Body.String())
	}
	return view
}

func TestMemoryRouteListsWhatIsStored(t *testing.T) {
	p, store := newRoutePlugin(t)
	route := routeFor(t, p, MemoryRoutePath)
	if route.Method() != http.MethodGet {
		t.Fatalf("method=%q, want GET", route.Method())
	}
	at := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.Remember("session-a", "prefers Go", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := store.Remember("session-b", "uses voice input", at.Add(time.Minute)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	w := serveRoute(t, route, http.MethodGet, "")
	view := decodeMemoryView(t, w)
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
	if strings.Contains(w.Body.String(), `"type"`) {
		t.Fatalf("the memory view leaks the storage record type: %s", w.Body.String())
	}

	// A retracted fact leaves the list and appears as retracted.
	if _, err := store.Retract(at, "prefers Go"); err != nil {
		t.Fatalf("Retract: %v", err)
	}
	view = decodeMemoryView(t, serveRoute(t, route, http.MethodGet, ""))
	if len(view.Facts) != 1 || view.Facts[0].Text != "uses voice input" {
		t.Fatalf("facts after a retraction=%+v", view.Facts)
	}
	if len(view.Retracted) != 1 || view.Retracted[0].Text != "prefers Go" || view.Retracted[0].RetractedAt.IsZero() {
		t.Fatalf("retracted=%+v", view.Retracted)
	}
}

func TestMemoryRouteIsEmptyForAFreshStore(t *testing.T) {
	p, _ := newRoutePlugin(t)
	w := serveRoute(t, routeFor(t, p, MemoryRoutePath), http.MethodGet, "")
	view := decodeMemoryView(t, w)
	if len(view.Facts) != 0 || len(view.Retracted) != 0 {
		t.Fatalf("fresh memory=%+v", view)
	}
	if body := w.Body.String(); strings.Contains(body, "null") {
		t.Fatalf("an empty memory must be an empty list, not null: %s", body)
	}
}

func TestRetractRouteRemovesOneFact(t *testing.T) {
	p, store := newRoutePlugin(t)
	route := routeFor(t, p, RetractRoutePath)
	if route.Method() != http.MethodPost {
		t.Fatalf("method=%q, want POST", route.Method())
	}
	at := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.Remember("session-a", "keep me", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if _, err := store.Remember("session-a", "drop me", at.Add(time.Minute)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	body := `{"at":"` + at.Add(time.Minute).Format(time.RFC3339Nano) + `","text":"drop me"}`
	w := serveRoute(t, route, http.MethodPost, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Retracted memoryRetractedFact `json:"retracted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if response.Retracted.Text != "drop me" || response.Retracted.RetractedAt.IsZero() {
		t.Fatalf("retracted=%+v", response.Retracted)
	}
	view := decodeMemoryView(t, serveRoute(t, routeFor(t, p, MemoryRoutePath), http.MethodGet, ""))
	if len(view.Facts) != 1 || view.Facts[0].Text != "keep me" {
		t.Fatalf("facts after retracting=%+v", view.Facts)
	}
}

func TestRetractRouteRefusesAFactThatIsNotInEffect(t *testing.T) {
	p, store := newRoutePlugin(t)
	route := routeFor(t, p, RetractRoutePath)
	at := time.Date(2026, time.September, 25, 10, 0, 0, 0, time.UTC)
	if _, err := store.Remember("s", "the only fact", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	body := `{"at":"` + at.Format(time.RFC3339Nano) + `","text":"a different fact"}`
	if w := serveRoute(t, route, http.MethodPost, body); w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (body=%s)", w.Code, w.Body.String())
	}
	// Retracting twice is the same case: the fact is no longer in effect.
	good := `{"at":"` + at.Format(time.RFC3339Nano) + `","text":"the only fact"}`
	if w := serveRoute(t, route, http.MethodPost, good); w.Code != http.StatusOK {
		t.Fatalf("first retraction status=%d body=%s", w.Code, w.Body.String())
	}
	if w := serveRoute(t, route, http.MethodPost, good); w.Code != http.StatusNotFound {
		t.Fatalf("second retraction status=%d, want 404", w.Code)
	}
}

func TestRetractRouteRejectsMalformedRequests(t *testing.T) {
	p, _ := newRoutePlugin(t)
	route := routeFor(t, p, RetractRoutePath)
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
		w := serveRoute(t, route, http.MethodPost, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400 (body=%s)", name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("%s: the error body must be {\"error\": \"...\"}: %s", name, w.Body.String())
		}
	}
}

// The route keeps its own guard on the body size, independent of the Kernel's.
func TestRetractRouteRejectsAnOversizedBody(t *testing.T) {
	p, _ := newRoutePlugin(t)
	route := routeFor(t, p, RetractRoutePath)
	body := `{"at":"2026-09-25T10:00:00Z","text":"` + strings.Repeat("x", 40000) + `"}`
	w := serveRoute(t, route, http.MethodPost, body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 413 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exceeds 32768 bytes") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestMemoryRouteReportsACorruptStoreWithoutAHostPath(t *testing.T) {
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
	p, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := serveRoute(t, routeFor(t, p, MemoryRoutePath), http.MethodGet, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), dir) {
		t.Fatalf("the error leaks a host path: %s", w.Body.String())
	}
	// The retract route reports the same corruption rather than silently
	// accepting a retraction it cannot match.
	body := `{"at":"2026-09-25T10:00:00Z","text":"x"}`
	if w := serveRoute(t, routeFor(t, p, RetractRoutePath), http.MethodPost, body); w.Code != http.StatusInternalServerError {
		t.Fatalf("retract on a corrupt store: status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	} else if strings.Contains(w.Body.String(), dir) {
		t.Fatalf("the error leaks a host path: %s", w.Body.String())
	}
}

func TestMemoryRoutesWithoutAStore(t *testing.T) {
	if w := serveRoute(t, factsRoute{}, http.MethodGet, ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (body=%s)", w.Code, w.Body.String())
	}
	if w := serveRoute(t, retractRoute{}, http.MethodPost, `{"at":"2026-09-25T10:00:00Z","text":"x"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("retract without a store: status=%d, want 500", w.Code)
	}
}
