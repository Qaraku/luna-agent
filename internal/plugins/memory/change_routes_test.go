package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMemoryViewFiltersScopeAndCarriesReferences(t *testing.T) {
	p, s := newRoutePlugin(t)
	s.RememberScoped(context.Background(), "s", "r", "global", ScopeGlobal, "", "model")
	s.RememberScoped(context.Background(), "s", "r", "project A", ScopeProject, "a", "model")
	s.RememberScoped(context.Background(), "s", "r", "project B", ScopeProject, "b", "model")
	q := httptest.NewRequest(http.MethodGet, "/api/memory?scope=project&workspace=a", nil)
	w := httptest.NewRecorder()
	routeFor(t, p, MemoryRoutePath).ServeHTTP(w, q)
	view := decodeMemoryView(t, w)
	if len(view.Facts) != 1 || view.Facts[0].Text != "project A" || view.Facts[0].Ref == "" || view.Facts[0].WorkspaceID != "a" {
		t.Fatalf("view=%+v", view)
	}
	if strings.Contains(w.Body.String(), "project B") {
		t.Fatal("project text leaked")
	}
}
func TestUserCorrectionAndRestorePreserveScopeAndServerOrigin(t *testing.T) {
	p, s := newRoutePlugin(t)
	fact, _ := s.RememberScoped(context.Background(), "s", "r", "old", ScopeProject, "a", "model")
	raw, _ := json.Marshal(map[string]any{"ref": fact.Ref(), "text": "corrected", "reason": "user correction"})
	w := serveRoute(t, routeFor(t, p, CorrectRoutePath), http.MethodPost, string(raw))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var payload struct{ Fact memoryFact }
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Fact.Origin != "user" || payload.Fact.WorkspaceID != "a" || payload.Fact.Ref == fact.Ref() {
		t.Fatalf("fact=%+v", payload.Fact)
	}
	raw, _ = json.Marshal(map[string]any{"ref": fact.Ref(), "expected_ref": payload.Fact.Ref, "reason": "undo"})
	w = serveRoute(t, routeFor(t, p, RestoreRoutePath), http.MethodPost, string(raw))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	snapshot, _ := s.Snapshot()
	if len(snapshot.Changes) != 2 || snapshot.Facts[0].Text != "old" {
		t.Fatal("restore failed")
	}
	forged := serveRoute(t, routeFor(t, p, CorrectRoutePath), http.MethodPost, `{"ref":"x","text":"bad","origin":"model"}`)
	if forged.Code != 400 {
		t.Fatal("forged origin accepted")
	}
}
func TestWithdrawnMemoryCanBeExplicitlyRestored(t *testing.T) {
	p, s := newRoutePlugin(t)
	fact, _ := s.RememberScoped(context.Background(), "s", "r", "kept", ScopeGlobal, "", "model")
	if _, err := s.RetractRef(fact.Ref()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"ref": fact.Ref(), "reason": "restore withdrawn note"})
	w := serveRoute(t, routeFor(t, p, RestoreRoutePath), http.MethodPost, string(raw))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	active, _ := s.Facts()
	if len(active) != 1 || active[0].Text != "kept" {
		t.Fatal("withdrawn note not restored")
	}
}
