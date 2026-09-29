package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistorySearchUsesMessageScopeNotLatestBinding(t *testing.T) {
	s, _ := Open(t.TempDir())
	id, _ := s.Create("history")
	a, b := "project-a", "project-b"
	s.AppendConfig(id, ConfigRecord{Workspace: a})
	s.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "needle in A"})
	s.AppendConfig(id, ConfigRecord{Workspace: b})
	s.AppendMessage(id, MessageRecord{Role: RoleAssistant, Text: "needle in B"})
	s.AppendToolCall(id, ToolCallRecord{Result: "needle private tool output"})
	got, err := s.Search(context.Background(), SearchOptions{Query: "NEEDLE", Workspace: &a})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 1 || got.Matches[0].Text != "needle in A" {
		t.Fatalf("scope search=%+v", got)
	}
	all, err := s.Search(context.Background(), SearchOptions{Query: "needle"})
	if err != nil || len(all.Matches) != 2 {
		t.Fatalf("message-only search=%+v %v", all, err)
	}
}
func TestBoundedTailDoesNotGuessUnknownLegacyScope(t *testing.T) {
	s, _ := Open(t.TempDir())
	id, _ := s.Create("large")
	scope := "project-a"
	s.AppendConfig(id, ConfigRecord{Workspace: scope})
	s.AppendToolCall(id, ToolCallRecord{Result: strings.Repeat("x", MaxSearchFileBytes+100)})
	s.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "needle unknown"})
	s.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "needle known", WorkspaceID: &scope})
	got, err := s.Search(context.Background(), SearchOptions{Query: "needle", Workspace: &scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 1 || got.Matches[0].Text != "needle known" || !got.Truncated || got.UnknownScope != 1 {
		t.Fatalf("tail search=%+v", got)
	}
	if got.ScannedBytes > MaxSearchTotalBytes {
		t.Fatal("byte budget exceeded")
	}
}
func TestHistorySearchIncludesArchivedAndBoundsResults(t *testing.T) {
	s, _ := Open(t.TempDir())
	id, _ := s.Create("archived")
	scope := ""
	for i := 0; i < 10; i++ {
		s.AppendMessage(id, MessageRecord{Role: RoleUser, Text: "needle " + strings.Repeat("文", 1500), WorkspaceID: &scope})
	}
	s.AppendConfig(id, ConfigRecord{Archived: true})
	got, err := s.Search(context.Background(), SearchOptions{Query: "needle", Workspace: &scope, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 2 || !got.Truncated {
		t.Fatalf("bounded archived search=%+v", got)
	}
	raw, _ := json.Marshal(got)
	if len(raw) > MaxSearchResultBytes {
		t.Fatal("result byte cap exceeded")
	}
}
func TestHistorySearchRejectsInvalidQueryAndSymlinkFiles(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, opts := range []SearchOptions{{Query: ""}, {Query: "x", SessionID: "../escape"}, {Query: "x", Limit: 51}} {
		if _, err := s.Search(context.Background(), opts); err == nil {
			t.Fatalf("invalid query accepted: %+v", opts)
		}
	}
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte(`{"type":"message","text":"needle private","role":"user"}`+"\n"), 0600)
	os.Symlink(outside, filepath.Join(s.Dir(), "aaaaaaaa.jsonl"))
	if _, err := s.Search(context.Background(), SearchOptions{Query: "needle", SessionID: "aaaaaaaa"}); err == nil {
		t.Fatal("followed a session symlink")
	}
}

func TestHistorySearchExcludesTheCurrentRunWithoutHidingOtherSessions(t *testing.T) {
	s, _ := Open(t.TempDir())
	current, _ := s.Create("current")
	other, _ := s.Create("other")
	s.AppendMessage(current, MessageRecord{RunID: "run-one", Role: RoleUser, Text: "needle current request"})
	s.AppendMessage(other, MessageRecord{RunID: "run-one", Role: RoleAssistant, Text: "needle older answer"})
	got, err := s.Search(context.Background(), SearchOptions{Query: "needle", ExcludeSessionID: current, ExcludeRunID: "run-one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 1 || got.Matches[0].SessionID != other {
		t.Fatalf("current run polluted history: %+v", got)
	}
}

func TestHeaderlessLegacyHistoryIsNotGuessedAsUnbound(t *testing.T) {
	s, _ := Open(t.TempDir())
	path := filepath.Join(s.Dir(), "aaaaaaaa"+FileSuffix)
	if err := os.WriteFile(path, []byte(`{"type":"message","role":"user","text":"needle without a scope"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := ""
	got, err := s.Search(context.Background(), SearchOptions{Query: "needle", Workspace: &scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 0 || got.UnknownScope != 1 {
		t.Fatalf("legacy scope was guessed: %+v", got)
	}
}
