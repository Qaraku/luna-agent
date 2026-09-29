package presets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/runconfig"
)

func TestPresetRevisionsAreImmutableAndRequireCurrentRevision(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	original := runconfig.Selection{ID: "personal", Title: "个人", Instructions: "简洁回答", Capabilities: []string{}}
	first, err := s.Save(original, "")
	if err != nil {
		t.Fatal(err)
	}
	original.Instructions = "列出关键取舍"
	next, err := s.Save(original, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision == first.Revision {
		t.Fatal("revision did not advance")
	}
	if _, err = s.Save(original, first.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write=%v", err)
	}
	old, err := s.Lookup("personal", first.Revision)
	if err != nil || old.Instructions != "简洁回答" {
		t.Fatalf("old revision lost: %+v %v", old, err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reopened.Lookup("personal", "")
	if err != nil || latest.Revision != next.Revision || latest.Capabilities == nil {
		t.Fatalf("reopen=%+v %v", latest, err)
	}
}

func TestArchiveAndRestorePreserveHistory(t *testing.T) {
	s, _ := Open(t.TempDir())
	first, err := s.Save(runconfig.Selection{ID: "personal", Title: "个人"}, "")
	if err != nil {
		t.Fatal(err)
	}
	archived, err := s.Archive("personal", first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lookup("personal", ""); !errors.Is(err, ErrArchived) {
		t.Fatalf("archive lookup=%v", err)
	}
	if _, err = s.Lookup("personal", first.Revision); err != nil {
		t.Fatal("pinned historical revision disappeared", err)
	}
	restored, err := s.Restore("personal", first.Revision, archived.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Archived || restored.Revision == first.Revision {
		t.Fatal("restore must append a new active revision")
	}
	history, err := s.History("personal")
	if err != nil || len(history) != 3 {
		t.Fatalf("history=%v %v", history, err)
	}
}

func TestBuiltinsAreReadOnlyAndCanBeCopied(t *testing.T) {
	s, _ := Open(t.TempDir())
	all, err := s.List(false)
	if err != nil || len(all) != 3 {
		t.Fatalf("builtins=%v %v", all, err)
	}
	for _, item := range all {
		if !item.Builtin || item.Owner != PluginID {
			t.Fatalf("builtin=%+v", item)
		}
		if _, err = s.Save(item.Selection, item.Revision); !errors.Is(err, ErrBuiltin) {
			t.Fatal("builtin was writable", err)
		}
	}
	copy := all[0].Selection
	copy.ID = "mine"
	copy.Revision = ""
	if _, err = s.Save(copy, ""); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidPresetCannotTouchOutsideStateRoot(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "presets"))
	for _, id := range []string{"../escape", "/tmp/escape", "a/b", ""} {
		if _, err := s.Save(runconfig.Selection{ID: id}, ""); err == nil {
			t.Fatal("invalid ID accepted", id)
		}
	}
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "presets", "evil.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(runconfig.Selection{ID: "evil"}, ""); err == nil {
		t.Fatal("followed symlink")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep" {
		t.Fatal("outside state changed")
	}
}

func TestCorruptRevisionIsNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(runconfig.Selection{ID: "broken"}, ""); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt write=%v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "broken.jsonl"))
	if string(data) != "{broken" {
		t.Fatal("corrupt history overwritten")
	}
}

func TestPresetLimitsFailWithoutDroppingRevisions(t *testing.T) {
	s, _ := Open(t.TempDir())
	var rev string
	for i := 0; i < MaxRevisions; i++ {
		entry, err := s.Save(runconfig.Selection{ID: "work", Title: "工作"}, rev)
		if err != nil {
			t.Fatal(err)
		}
		rev = entry.Revision
	}
	if _, err := s.Save(runconfig.Selection{ID: "work"}, rev); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal("missing revision limit", err)
	}
	history, err := s.History("work")
	if err != nil || len(history) != MaxRevisions {
		t.Fatal("history was evicted", err)
	}
}
