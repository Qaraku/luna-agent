package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripKeepsTheValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	want := File{BaseURL: "https://api.example.com/v1", APIKey: "sk-secret-value", Model: "some-model"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the file was written but not found")
	}
	if got != want {
		t.Fatalf("loaded %+v, want %+v", got, want)
	}
}

// The file holds a key, so its permission is part of what it is. A provider file
// the whole machine can read is a leaked key.
func TestTheFileIsWrittenPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, File{BaseURL: "https://api.example.com/v1", APIKey: "k", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

// A Luna that has never been configured is a normal Luna: the first run must not
// fail because a file nobody created is missing.
func TestAMissingFileIsNotAnError(t *testing.T) {
	file, found, err := LoadFile(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("found a file that is not there")
	}
	if !file.Empty() {
		t.Fatalf("file = %+v, want empty", file)
	}
}

func TestAnEmptyFileIsAnEmptyProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !file.Empty() {
		t.Fatalf("found %v, file %+v; want found and empty", found, file)
	}
}

// A key pasted into a form arrives with a newline often enough that keeping it
// would produce an authentication failure with no visible cause.
func TestSurroundingSpaceIsNotPartOfAValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("base_url: \" https://api.example.com/v1 \"\napi_key: \"  sk-abc  \"\nmodel: \" m \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := File{BaseURL: "https://api.example.com/v1", APIKey: "sk-abc", Model: "m"}
	if file != want {
		t.Fatalf("file = %+v, want %+v", file, want)
	}
}

// A misspelled key that silently does nothing leaves the user with no effect and
// no explanation — the same reason config.yaml refuses unknown keys.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("base_url: https://api.example.com/v1\napi_ky: sk-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadFile(path); err == nil {
		t.Fatal("a misspelled key was accepted")
	}
}

// Save validates first, so a file that could not be read back never reaches the
// disk — and a rejected save must not leave a half-configured provider behind.
func TestSaveRefusesAFileItCouldNotReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, File{BaseURL: "api.example.com/v1"}); err == nil {
		t.Fatal("Save accepted a relative base URL")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused save wrote something: %v", err)
	}
}

func TestValidateAcceptsAPartialFile(t *testing.T) {
	// The settings page saves the endpoint before it knows the model, so
	// "incomplete" has to be a state this package can represent and accept.
	for _, file := range []File{
		{BaseURL: "https://api.example.com/v1"},
		{APIKey: "sk-abc"},
		{Model: "m"},
		{},
	} {
		if err := file.Validate(); err != nil {
			t.Fatalf("Validate(%+v) = %v, want nil", file, err)
		}
	}
}

func TestValidateRefusesAKeyWithALineBreak(t *testing.T) {
	if err := (File{APIKey: "sk-abc\nextra"}).Validate(); err == nil {
		t.Fatal("Validate accepted a key containing a newline")
	}
}

// A hint has to be recognisable without being usable: the person who set the key
// can tell which one it is, and nobody can call a provider with it.
func TestTheHintShowsOnlyTheEnd(t *testing.T) {
	hint := Hint("sk-1234567890abcd")
	if strings.Contains(hint, "1234567890") {
		t.Fatalf("hint %q discloses the key", hint)
	}
	if !strings.HasSuffix(hint, "abcd") {
		t.Fatalf("hint %q should end with the last four characters", hint)
	}
	if hint = Hint(""); hint != "" {
		t.Fatalf("hint about an unset key = %q, want empty", hint)
	}
	if hint = Hint("abc"); strings.Contains(hint, "abc") {
		t.Fatalf("hint %q discloses a short key", hint)
	}
}
