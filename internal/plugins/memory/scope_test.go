package memory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScopedMemoryDoesNotReclassifyLegacyAndSeparatesProjects(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.jsonl"))
	legacy, err := s.Remember("old-session", "legacy global", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.path)
	a, err := s.RememberScoped(context.Background(), "session-a", "run-a", "project A", ScopeProject, "project-a", "model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RememberScoped(context.Background(), "session-b", "run-b", "project B", ScopeProject, "project-b", "model"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	visible, err := filterFacts(snapshot.Facts, "current", "project-a")
	if err != nil || len(visible) != 2 || visible[0].Text != legacy.Text || visible[1].Ref() != a.Ref() {
		t.Fatalf("visible=%+v %v", visible, err)
	}
	after, _ := os.ReadFile(s.path)
	if !bytes.HasPrefix(after, before) {
		t.Fatal("scope upgrade rewrote legacy records")
	}
}
func TestMemoryCorrectionAndRestoreAreAppendOnlyAndRejectStaleRefs(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.jsonl"))
	first, err := s.RememberScoped(context.Background(), "s", "r", "old", ScopeProject, "p", "model")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.path)
	change, err := s.Correct(context.Background(), first.Ref(), "new", ChangeOrigin{Kind: "user", Reason: "correction"})
	if err != nil {
		t.Fatal(err)
	}
	if change.Before.Text != "old" || change.After.Text != "new" || change.After.WorkspaceID != "p" {
		t.Fatalf("change=%+v", change)
	}
	if _, err = s.Correct(context.Background(), first.Ref(), "stale", ChangeOrigin{Kind: "user"}); err != ErrMemoryConflict {
		t.Fatalf("stale correction=%v", err)
	}
	restored, err := s.Restore(context.Background(), first.Ref(), change.After.Ref(), ChangeOrigin{Kind: "user", Reason: "undo"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot()
	if err != nil || len(snapshot.Facts) != 1 || snapshot.Facts[0].Text != "old" || snapshot.Facts[0].Ref() != restored.After.Ref() || len(snapshot.Changes) != 2 {
		t.Fatalf("snapshot=%+v %v", snapshot, err)
	}
	after, _ := os.ReadFile(s.path)
	if !bytes.HasPrefix(after, before) {
		t.Fatal("correction rewrote old records")
	}
}
func TestMemoryRestoreCannotOverwriteUnrelatedFact(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.jsonl"))
	a, _ := s.RememberScoped(context.Background(), "s", "r", "A", ScopeGlobal, "", "model")
	b, _ := s.RememberScoped(context.Background(), "s", "r", "B", ScopeGlobal, "", "model")
	if _, err := s.Restore(context.Background(), a.Ref(), b.Ref(), ChangeOrigin{Kind: "user"}); err != ErrMemoryConflict {
		t.Fatalf("cross-family restore=%v", err)
	}
}
func TestModernMemoryCapacityNeverDropsCorrectionHistory(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "memory.jsonl"))
	fact, _ := s.RememberScoped(context.Background(), "s", "r", "start", ScopeGlobal, "", "model")
	for i := 0; i < MaxMemoryChanges; i++ {
		change, err := s.Correct(context.Background(), fact.Ref(), "revised", ChangeOrigin{Kind: "user"})
		if err != nil {
			t.Fatal(err)
		}
		fact = change.After
	}
	before, _ := os.ReadFile(s.path)
	if _, err := s.Correct(context.Background(), fact.Ref(), "too many", ChangeOrigin{Kind: "user"}); !errors.Is(err, ErrMemoryCapacity) {
		t.Fatalf("limit=%v", err)
	}
	after, _ := os.ReadFile(s.path)
	if !bytes.Equal(before, after) {
		t.Fatal("capacity error changed history")
	}
}
