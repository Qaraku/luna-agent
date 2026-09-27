package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewValidatesDirectories(t *testing.T) {
	absolute := t.TempDir()
	cases := []struct {
		name string
		dirs []string
	}{
		{"relative path", []string{"relative/dir"}},
		{"one of several is relative", []string{absolute, "./other"}},
		{"no directories", nil},
		{"a blank directory", []string{"  "}},
	}
	for _, tc := range cases {
		if _, err := New("label", tc.dirs); err == nil {
			t.Errorf("%s: New accepted %v", tc.name, tc.dirs)
		}
	}
}

func TestNewKeepsOrderAndDropsRepeats(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	got, err := New("", []string{first, second, first})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := []string{first, second}
	if len(got.Dirs) != len(want) || got.Dirs[0] != want[0] || got.Dirs[1] != want[1] {
		t.Fatalf("dirs = %v, want %v", got.Dirs, want)
	}
}

func TestNewNamesTheWorkspaceAfterItsFirstDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "luna-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	got, err := New("  ", []string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Name != "luna-agent" {
		t.Fatalf("name = %q, want %q", got.Name, "luna-agent")
	}
}

func TestNewRefusesABlankNameNoDirectoryCanSupply(t *testing.T) {
	if _, err := New("  ", []string{string(filepath.Separator)}); err == nil {
		t.Fatal("New accepted a root directory with a blank name")
	}
}

func TestIDsAreRandomAndDistinct(t *testing.T) {
	dir := t.TempDir()
	first, err := New("one", []string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := New("two", []string{dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("two workspaces share the id %q", first.ID)
	}
	if len(first.ID) != IDBytes*2 {
		t.Fatalf("id %q is %d characters, want %d", first.ID, len(first.ID), IDBytes*2)
	}
}

func TestStoreRoundTripsAWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("a missing file gave %d workspaces", len(store.List()))
	}
	dir := t.TempDir()
	created, err := store.Create("luna", []string{dir})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, found := reopened.Get(created.ID)
	if !found {
		t.Fatalf("workspace %s is not in the reopened store", created.ID)
	}
	if got.Name != "luna" || len(got.Dirs) != 1 || got.Dirs[0] != dir {
		t.Fatalf("reopened = %+v, want name luna and dir %s", got, dir)
	}
}

func TestCreateRefusesARepeatedNameAndNamesTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dir := t.TempDir()
	first, err := store.Create("luna", []string{dir})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = store.Create("luna", []string{dir})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("second Create: %v, want ErrNameTaken", err)
	}
	if !strings.Contains(err.Error(), first.ID) {
		t.Fatalf("error %q does not name the workspace holding the name (%s)", err, first.ID)
	}
	if len(store.List()) != 1 {
		t.Fatalf("store holds %d workspaces, want 1", len(store.List()))
	}
}

func TestOpenRefusesABrokenFileInsteadOfReadingItAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	dir := t.TempDir()
	if err := os.WriteFile(path, []byte(`{"workspaces":[{"id":"a","name":"a","dirs":["`+dir+`"]}]`), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a truncated file")
	}
	if err := os.WriteFile(path, []byte(`{"workspaces":[{"id":"a","name":"a","dirs":["`+dir+`"],"extra":1}]}`), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted an unknown field")
	}
}
