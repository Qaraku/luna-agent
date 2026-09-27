package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "luna", FileName)
}

func TestAMissingFileIsNotAnError(t *testing.T) {
	file, found, err := Load(settingsPath(t))
	if err != nil {
		t.Fatalf("a missing settings file is the normal first run: %v", err)
	}
	if found {
		t.Fatal("found reported true for a file that is not there")
	}
	if len(file.DisabledSkills()) != 0 {
		t.Fatalf("disabled=%v, want none", file.DisabledSkills())
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := settingsPath(t)
	want := Settings{Skills: Skills{Disabled: []string{"alpha", "beta"}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the file was just written but Load reports it is not there")
	}
	if names := got.DisabledSkills(); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("disabled=%v, want [alpha beta]", names)
	}
}

func TestTheWrittenFileHasTheDocumentedShape(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Skills: Skills{Disabled: []string{"alpha"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "skills:\n  disabled:\n    - alpha\n"
	if string(data) != want {
		t.Fatalf("file=%q, want %q", data, want)
	}
	// Settings with nothing turned off say nothing, rather than an empty list
	// that the next reader has to interpret.
	if err := Save(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "disabled") {
		t.Fatalf("file=%q, want no disabled key when nothing is turned off", data)
	}
}

func TestTheRewriteLeavesNoTemporaryFile(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Skills: Skills{Disabled: []string{"alpha"}}}); err != nil {
		t.Fatal(err)
	}
	// A second write is the case that would leave a temporary file behind if
	// the rename were skipped or the file were closed after it.
	if err := Save(path, Settings{Skills: Skills{Disabled: []string{"beta"}}}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory holds %v, want only %s", names, FileName)
	}
	file, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if names := file.DisabledSkills(); len(names) != 1 || names[0] != "beta" {
		t.Fatalf("disabled=%v, want only the second write's entry", names)
	}
}

func TestTheDirectoryAndFileArePrivate(t *testing.T) {
	path := settingsPath(t)
	dir := filepath.Dir(path)
	if err := Save(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("directory mode=%o, want 700", mode)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode=%o, want 600", mode)
	}
}

func TestAnUnknownKeyIsRefused(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("skills:\n  disabled:\n    - alpha\nskils:\n  disabled: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := Load(path)
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if found {
		t.Fatal("a file that failed to load reported itself as found")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Fatalf("err=%v, want it to name the file", err)
	}
	if strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("err=%v, want no host path", err)
	}
}

func TestAReadFailureNamesTheFileOnly(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file is expected: readable name, unreadable
	// content. The message must name the file and nothing about where it
	// lives on this host.
	path := filepath.Join(dir, FileName)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("reading a directory as the settings file was accepted")
	}
	if !strings.Contains(err.Error(), FileName) || strings.Contains(err.Error(), dir) {
		t.Fatalf("err=%v, want the file name and no host path", err)
	}
}

func TestBlankAndRepeatedNamesAreDropped(t *testing.T) {
	path := settingsPath(t)
	file := Settings{Skills: Skills{Disabled: []string{" alpha ", "", "alpha", "beta", "beta"}}}
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if names := got.DisabledSkills(); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("disabled=%v, want [alpha beta] once each", names)
	}
}

func TestTurningOneSkillOffAndOn(t *testing.T) {
	file := Settings{Skills: Skills{Disabled: []string{"alpha", "beta"}}}
	off := file.WithSkillDisabled("gamma", true)
	if names := off.DisabledSkills(); len(names) != 3 || names[2] != "gamma" {
		t.Fatalf("disabled=%v, want gamma appended", names)
	}
	back := off.WithSkillDisabled("alpha", false)
	if names := back.DisabledSkills(); len(names) != 2 || names[0] != "beta" || names[1] != "gamma" {
		t.Fatalf("disabled=%v, want alpha gone and the rest in place", names)
	}
	// Turning off what is already off changes nothing, and an empty name is not
	// a skill.
	if names := back.WithSkillDisabled("beta", true).DisabledSkills(); len(names) != 2 {
		t.Fatalf("disabled=%v, want no repeat", names)
	}
	if names := back.WithSkillDisabled("  ", true).DisabledSkills(); len(names) != 2 {
		t.Fatalf("disabled=%v, want a blank name ignored", names)
	}
}

func TestAnEmptyFileIsAnEmptySetting(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(file.DisabledSkills()) != 0 {
		t.Fatalf("found=%v disabled=%v, want an empty setting", found, file.DisabledSkills())
	}
}
