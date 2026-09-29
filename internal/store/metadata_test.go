package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionMetadataIsAppendOnlyAndPreservesRuntimeChoices(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Create("")
	if err = s.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "原来的标题"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(), id+FileSuffix)
	before, _ := os.ReadFile(path)
	title := "手动标题"
	cfg := ConfigRecord{Model: "model-one", Workspace: "project-one", TitleOverride: &title, Archived: true}
	if err = s.AppendConfig(id, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != title || !got.Archived || got.Config.Model != "model-one" {
		t.Fatalf("session=%+v", got)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || !list[0].Archived || list[0].Workspace != "project-one" {
		t.Fatalf("summaries=%+v %v", list, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.HasPrefix(after, before) {
		t.Fatal("metadata rewrote earlier records")
	}
	cfg.Archived = false
	if err = s.AppendConfig(id, cfg); err != nil {
		t.Fatal(err)
	}
	restored, _ := s.Read(id)
	if restored.Archived || restored.Title != title {
		t.Fatal("restore lost title or stayed archived")
	}
}
func TestMessageWorkspaceDistinguishesLegacyAndExplicitUnbound(t *testing.T) {
	s, _ := Open(t.TempDir())
	id, _ := s.Create("scope")
	empty := ""
	project := "project-one"
	for _, scope := range []*string{nil, &empty, &project} {
		if err := s.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "message", WorkspaceID: scope}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.Messages(id)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].WorkspaceID != nil || rows[1].WorkspaceID == nil || *rows[1].WorkspaceID != "" || *rows[2].WorkspaceID != "project-one" {
		t.Fatalf("scope lost: %+v", rows)
	}
}
