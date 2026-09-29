package skills

import (
	"context"
	"errors"
	catalog "github.com/Qaraku/luna-agent/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLibraryPublishesStandardFilesAndPreservesRevisions(t *testing.T) {
	dir := t.TempDir()
	lib, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := SkillDefinition{Name: "deploy", Description: "Deploy safely", Body: "# Steps\nRead before changing.\n", Files: map[string]string{"references/checks.md": "Check outputs.\n"}}
	first, err := lib.Save(d, "", SkillOrigin{Kind: "user", Reason: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(lib.Directory(first)) != d.Name {
		t.Fatal("managed skill directory must retain the standard skill name")
	}
	body, err := os.ReadFile(filepath.Join(lib.Directory(first), "SKILL.md"))
	if err != nil || !strings.Contains(string(body), "name: deploy") || !strings.Contains(string(body), d.Body) {
		t.Fatalf("standard file=%q err=%v", body, err)
	}
	found, problems := catalog.Discover([]catalog.Root{{Path: filepath.Dir(lib.Directory(first)), Scope: catalog.ScopeUser}})
	if len(problems) != 0 || len(found) != 1 || found[0].Name != d.Name {
		t.Fatalf("standard discovery=%+v problems=%v", found, problems)
	}
	d.Body = "# Steps\nNew method.\n"
	second, err := lib.Save(d, first.Revision, SkillOrigin{Kind: "model", SessionID: "aaaaaaaa", RunID: "run-one", Reason: "user correction"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := lib.Definition("deploy", first.Revision)
	if err != nil || old.Body == d.Body {
		t.Fatal("old revision changed", err)
	}
	if _, err = lib.Save(d, first.Revision, SkillOrigin{Kind: "user"}); !errors.Is(err, ErrLibraryConflict) {
		t.Fatalf("stale update=%v", err)
	}
	reopened, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History("deploy")
	if err != nil || len(history) != 2 || history[1].Revision != second.Revision || history[1].Origin.SessionID != "aaaaaaaa" {
		t.Fatalf("history=%+v %v", history, err)
	}
}
func TestLibraryPreviewDoesNotWriteAndRestoreAppends(t *testing.T) {
	dir := t.TempDir()
	lib, _ := OpenLibrary(dir)
	d := SkillDefinition{Name: "work", Description: "Work", Body: "old\n"}
	preview, err := lib.Preview(d, "")
	if err != nil || !strings.Contains(preview.Diff, "+old") {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("preview wrote files")
	}
	a, err := lib.Save(d, "", SkillOrigin{Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	d.Body = "new\n"
	b, err := lib.Save(d, a.Revision, SkillOrigin{Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := lib.Restore("work", a.Revision, b.Revision, SkillOrigin{Kind: "user", Reason: "restore"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := lib.Definition("work", restored.Revision)
	if err != nil || got.Body != "old\n" {
		t.Fatalf("restored=%+v %v", got, err)
	}
	history, _ := lib.History("work")
	if len(history) != 3 || restored.Parent != b.Revision {
		t.Fatal("restore rewrote history")
	}
}
func TestLibraryRejectsInvalidNamesFilesAndTampering(t *testing.T) {
	dir := t.TempDir()
	lib, _ := OpenLibrary(dir)
	for _, d := range []SkillDefinition{{Name: "../escape", Description: "x", Body: "x"}, {Name: "good", Description: "x", Body: "x", Files: map[string]string{"../outside": "x"}}, {Name: "good", Description: "x", Body: "x", Files: map[string]string{"run.sh": "echo nope"}}, {Name: "good", Description: "x", Body: "\x00"}} {
		if _, err := lib.Save(d, "", SkillOrigin{Kind: "user"}); err == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
	e, err := lib.Save(SkillDefinition{Name: "good", Description: "x", Body: "original"}, "", SkillOrigin{Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(lib.Directory(e), "SKILL.md")
	if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = lib.Definition("good", e.Revision); !errors.Is(err, ErrLibraryCorrupt) {
		t.Fatalf("tampered revision accepted: %v", err)
	}
}
func TestLibraryCorruptionCannotBeOverwritten(t *testing.T) {
	dir := t.TempDir()
	lib, _ := OpenLibrary(dir)
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("{partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Save(SkillDefinition{Name: "broken", Description: "x", Body: "x"}, "", SkillOrigin{Kind: "user"}); !errors.Is(err, ErrLibraryCorrupt) {
		t.Fatalf("corrupt save=%v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "broken.jsonl"))
	if string(data) != "{partial" {
		t.Fatal("corruption overwritten")
	}
}

func TestLibraryRejectsRevisionDirectorySymlinkEscape(t *testing.T) {
	lib, _ := OpenLibrary(t.TempDir())
	e, err := lib.Save(SkillDefinition{Name: "work", Description: "Work", Body: "original"}, "", SkillOrigin{Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	path := lib.Directory(e)
	if err = os.Rename(path, path+"-kept"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err = os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err = lib.Definition("work", e.Revision); !errors.Is(err, ErrLibraryCorrupt) {
		t.Fatalf("escaped revision root: %v", err)
	}
}

func TestReferenceFileExtensionMustBeLiteralMarkdown(t *testing.T) {
	lib, _ := OpenLibrary(t.TempDir())
	_, err := lib.Save(SkillDefinition{Name: "work", Description: "Work", Body: "steps", Files: map[string]string{"references/oddxmd": "not a Markdown extension"}}, "", SkillOrigin{Kind: "user"})
	if err == nil {
		t.Fatal("non-Markdown extension accepted")
	}
}

func TestCancelledQueuedSaveDoesNotPublish(t *testing.T) {
	lib, _ := OpenLibrary(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	lib.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := lib.SaveContext(ctx, SkillDefinition{Name: "queued", Description: "Work", Body: "steps"}, "", SkillOrigin{Kind: "user"})
		done <- err
	}()
	cancel()
	lib.mu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled save=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("save did not return")
	}
	files, _ := os.ReadDir(lib.dir)
	if len(files) != 0 {
		t.Fatal("cancelled queued save changed the library")
	}
}
