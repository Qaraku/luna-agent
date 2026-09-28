package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// configured is one named provider, which is the shape everything below is built
// from: the smallest file that is both listed and active.
func configured() File {
	return File{
		Active: "deepseek",
		Providers: map[string]Endpoint{
			"deepseek": {
				BaseURL: "https://api.example.com/v1",
				APIKey:  "sk-secret-value",
				Model:   "some-model",
				Models:  []string{"some-model-fast", "some-model-pro"},
			},
		},
	}
}

func TestRoundTripKeepsTheValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	want := configured()
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
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded %+v, want %+v", got, want)
	}
}

// A provider with several models may write a whole file and read the same thing
// back, including which of them is active.
func TestSeveralProvidersSurviveARoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	want := configured()
	want.Providers["backup"] = Endpoint{BaseURL: "https://backup.example.com/v1", APIKey: "sk-backup", Model: "backup-model"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded %+v, want %+v", got, want)
	}
	name, entry, ok := got.Selection()
	if !ok || name != "deepseek" || entry.Model != "some-model" {
		t.Fatalf("selection = %q, %+v, %v", name, entry, ok)
	}
}

// The file holds keys, so its permission is part of what it is. A provider file
// the whole machine can read is a leaked key.
func TestTheFileIsWrittenPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, configured()); err != nil {
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
	if names := file.Names(); len(names) != 0 {
		t.Fatalf("names = %v, want none", names)
	}
	if _, _, ok := file.Selection(); ok {
		t.Fatal("an empty file reported an active provider")
	}
}

func TestAnEmptyFileIsAnEmptyProvider(t *testing.T) {
	for name, body := range map[string]string{"blank": "\n", "empty mapping": "{}\n"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			file, found, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !found || !file.Empty() {
				t.Fatalf("found %v, file %+v; want found and empty", found, file)
			}
		})
	}
}

// A key pasted into a form arrives with a newline often enough that keeping it
// would produce an authentication failure with no visible cause.
func TestSurroundingSpaceIsNotPartOfAValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := "active: \" deepseek \"\nproviders:\n  \" deepseek \":\n    base_url: \" https://api.example.com/v1 \"\n    api_key: \"  sk-abc  \"\n    model: \" m \"\n    models:\n      - \" m2 \"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := File{
		Active:    "deepseek",
		Providers: map[string]Endpoint{"deepseek": {BaseURL: "https://api.example.com/v1", APIKey: "sk-abc", Model: "m", Models: []string{"m2"}}},
	}
	if !reflect.DeepEqual(file, want) {
		t.Fatalf("file = %+v, want %+v", file, want)
	}
}

// A misspelled key that silently does nothing leaves the user with no effect and
// no explanation — the same reason config.yaml refuses unknown keys.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("active: a\nproviders:\n  a:\n    base_ur: https://api.example.com/v1\n"), 0o600); err != nil {
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
	bad := File{Active: "a", Providers: map[string]Endpoint{"a": {BaseURL: "api.example.com/v1"}}}
	if err := Save(path, bad); err == nil {
		t.Fatal("Save accepted a relative base URL")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused save wrote something: %v", err)
	}
}

func TestValidateAcceptsAPartialProvider(t *testing.T) {
	// The settings page saves the endpoint before it knows the model, so
	// "incomplete" has to be a state this package can represent and accept.
	for _, file := range []File{
		{},
		{Active: "a", Providers: map[string]Endpoint{"a": {BaseURL: "https://api.example.com/v1"}}},
		{Active: "a", Providers: map[string]Endpoint{"a": {APIKey: "sk-abc"}}},
		{Active: "a", Providers: map[string]Endpoint{"a": {Model: "m"}}},
	} {
		if err := file.Validate(); err != nil {
			t.Fatalf("Validate(%+v) = %v, want nil", file, err)
		}
	}
}

// An active name that points at nothing is refused rather than read as "no
// provider": the file would otherwise mean something different from what it says,
// and a run would be sent to a provider the user did not name.
func TestActiveMustNameAListedProvider(t *testing.T) {
	cases := []struct {
		name string
		file File
		want string
	}{
		{"dangling active", File{Active: "gone", Providers: map[string]Endpoint{"a": {}}}, "gone"},
		{"active without providers", File{Active: "a"}, "a"},
		{"providers without active", File{Providers: map[string]Endpoint{"a": {}}}, "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.file.Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", tc.file)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// A name is how a form, a log line and a run all refer to the same endpoint, so
// it has to be there and has to be one token.
func TestProviderNamesMustBeUsable(t *testing.T) {
	cases := []struct {
		name string
		file File
	}{
		{"empty name", File{Active: "a", Providers: map[string]Endpoint{"": {Model: "m"}}}},
		{"whitespace inside the name", File{Active: "a b", Providers: map[string]Endpoint{"a b": {Model: "m"}}}},
		{"name that is only space", File{Active: "a", Providers: map[string]Endpoint{"  ": {Model: "m"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.file.Validate(); err == nil {
				t.Fatalf("%+v was accepted", tc.file)
			}
		})
	}
}

func TestValidateRefusesAKeyWithALineBreak(t *testing.T) {
	if err := (File{Active: "a", Providers: map[string]Endpoint{"a": {APIKey: "sk-abc\nextra"}}}).Validate(); err == nil {
		t.Fatal("Validate accepted a key containing a newline")
	}
}

// The models a provider may be asked for are a set, and a name that appears twice
// cannot be chosen: the two candidates would be indistinguishable. The refusal
// names the entry rather than dropping it quietly, because a user who typed it
// has to be told which one is the problem.
func TestModelsMustBeAUsableSet(t *testing.T) {
	cases := []struct {
		name  string
		entry Endpoint
		want  string
	}{
		{"empty entry", Endpoint{Model: "m", Models: []string{"m2", "  "}}, "empty entry"},
		{"listed twice", Endpoint{Model: "m", Models: []string{"m2", "m2"}}, `"m2" twice`},
		{"the same as the model", Endpoint{Model: "m", Models: []string{"m"}}, "already the model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (File{Active: "a", Providers: map[string]Endpoint{"a": tc.entry}}).Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", tc.entry)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// The names are the order an interface renders them in, so they are sorted rather
// than whatever the map happened to yield.
func TestNamesAreSorted(t *testing.T) {
	file := File{Active: "b", Providers: map[string]Endpoint{"c": {}, "a": {}, "b": {}}}
	want := []string{"a", "b", "c"}
	if got := file.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
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

// A file an earlier version wrote — one endpoint, one key and one model at the top
// level — is read as one provider, so an installation that was already configured
// keeps working instead of being told its own file is broken on startup. It gets a
// name, because everything above this point refers to a provider by name, and the
// next save writes the current shape.
func TestThePreviousShapeIsReadAsOneProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	previous := "base_url: https://old.example.test/v1\napi_key: sk-old-value\nmodel: old-model\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := LoadFile(path)
	if err != nil {
		t.Fatalf("a file an earlier version wrote must be read: %v", err)
	}
	if !found {
		t.Fatal("the file was there but not found")
	}
	if file.Active != LegacyName {
		t.Fatalf("active = %q, want %q", file.Active, LegacyName)
	}
	entry, ok := file.Providers[LegacyName]
	if !ok {
		t.Fatalf("providers = %+v", file.Providers)
	}
	if entry.BaseURL != "https://old.example.test/v1" || entry.APIKey != "sk-old-value" || entry.Model != "old-model" {
		t.Fatalf("entry = %+v", entry)
	}
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "providers:") {
		t.Fatalf("the save did not write the current shape: %s", written)
	}
	again, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, file) {
		t.Fatalf("round trip = %+v, want %+v", again, file)
	}
}

// The error has to point at the file the person actually has: one misspelled key in
// an older file must name that key, not complain that the current shape has no
// field called base_url.
func TestAMisspelledKeyInThePreviousShapeIsNamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("base_url: https://old.example.test/v1\nmodle: typo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "modle") {
		t.Fatalf("err = %v, want it to name the misspelled key", err)
	}
}

// A file that is neither shape is refused by the current decoder, which names the
// keys it does not expect.
func TestAFileOfNeitherShapeNamesTheUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("base_urls: https://old.example.test/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "base_urls") {
		t.Fatalf("err = %v, want it to name the unknown key", err)
	}
}

// A file that carries both shapes is the current one with an unexpected key:
// telling the shapes apart by their keys must not make base_url acceptable next to
// providers.
func TestAFileThatCarriesBothShapesIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	mixed := "active: a\nproviders:\n  a:\n    base_url: https://a.example.test/v1\n    model: m\nbase_url: https://old.example.test/v1\n"
	if err := os.WriteFile(path, []byte(mixed), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("err = %v, want it to refuse the stray key", err)
	}
}
