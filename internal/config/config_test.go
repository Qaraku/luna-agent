package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isEmptyFile reports whether a file carries nothing, which is what a missing or
// an empty file must produce. File is not comparable itself once it holds a
// slice, so the fields are checked rather than the struct.
func isEmptyFile(f File) bool {
	return f.Model == "" && f.BaseURL == "" && f.APIKeyEnv == "" && f.ReasoningEffort == "" && len(f.Models) == 0
}

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

func TestLoadAcceptsAgreeingModelAliases(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"OPENAI_BASE_URL":   "https://example.test/v1",
		"OPENAI_API_KEY":    "secret-value",
		"OPENAI_MODEL_NAME": "demo",
		"OPENAI_MODEL":      "demo",
		"OPENAI_MODEL_ID":   "demo",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "demo" || cfg.ProviderHost != "example.test" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadRejectsConflictingAliasesWithoutValues(t *testing.T) {
	_, err := Load(env(map[string]string{
		"OPENAI_BASE_URL": "https://example.test/v1", "OPENAI_API_KEY": "top-secret",
		"OPENAI_MODEL_NAME": "model-a", "OPENAI_MODEL": "model-b",
	}), nil)
	if err == nil {
		t.Fatal("expected conflict")
	}
	msg := err.Error()
	for _, value := range []string{"model-a", "model-b", "top-secret"} {
		if strings.Contains(msg, value) {
			t.Fatalf("error leaked value %q: %s", value, msg)
		}
	}
	if !strings.Contains(msg, "OPENAI_MODEL_NAME") || !strings.Contains(msg, "OPENAI_MODEL") {
		t.Fatalf("missing variable names: %s", msg)
	}
}

func TestLoadReportsMissingNamesOnly(t *testing.T) {
	_, err := Load(env(map[string]string{}), nil)
	if err == nil {
		t.Fatal("expected missing error")
	}
	for _, name := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL_NAME/OPENAI_MODEL/OPENAI_MODEL_ID"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("missing %s in %s", name, err)
		}
	}
}

func effortEnv(level string) func(string) string {
	return env(map[string]string{
		"OPENAI_BASE_URL":   "https://example.test/v1",
		"OPENAI_API_KEY":    "test-key",
		"OPENAI_MODEL_NAME": "test-model",
		ReasoningEffortEnv:  level,
	})
}

func TestLoadCarriesTheChosenReasoningEffort(t *testing.T) {
	cfg, err := Load(effortEnv("  High "), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "high" {
		t.Fatalf("got %q, want the declared level lowercased", cfg.ReasoningEffort)
	}
}

func TestLoadSendsNoReasoningEffortByDefault(t *testing.T) {
	cfg, err := Load(effortEnv(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "" {
		t.Fatalf("got %q, want nothing chosen", cfg.ReasoningEffort)
	}
}

func TestLoadRejectsALevelTheAPIDoesNotDefine(t *testing.T) {
	for _, level := range []string{"ultra", "xhigh", "max"} {
		_, err := Load(effortEnv(level), nil)
		if err == nil {
			t.Fatalf("%q was accepted", level)
		}
		if !strings.Contains(err.Error(), ReasoningEffortEnv) {
			t.Fatalf("%q: the error does not name the variable: %v", level, err)
		}
		if strings.Contains(err.Error(), level) {
			t.Fatalf("%q: the error quotes the rejected value: %v", level, err)
		}
	}
}

// fileEnv is an environment that is complete on its own, so a test can show what
// the file changes rather than what it fills in.
func fileEnv() func(string) string {
	return env(map[string]string{
		"OPENAI_BASE_URL":   "https://env.example.test/v1",
		"OPENAI_API_KEY":    "env-key",
		"OPENAI_MODEL_NAME": "env-model",
	})
}

func TestTheFileOverridesTheEnvironment(t *testing.T) {
	cfg, err := Load(fileEnv(), &File{
		Model:           "file-model",
		BaseURL:         "https://file.example.test/v1",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "file-model" || cfg.ProviderHost != "file.example.test" || cfg.ReasoningEffort != "high" {
		t.Fatalf("the environment won where the file stated a value: %#v", cfg)
	}
	if cfg.APIKey != "env-key" {
		t.Fatal("the key must still come from the environment variable the file names")
	}
}

// The point of the file is that a user can configure Luna without a launcher
// that exports variables.
func TestTheFileSuppliesWhatTheEnvironmentLacks(t *testing.T) {
	cfg, err := Load(env(map[string]string{"OPENAI_API_KEY": "only-key"}), &File{
		Model:   "file-model",
		BaseURL: "https://file.example.test/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "file-model" || cfg.ProviderHost != "file.example.test" || cfg.APIKey != "only-key" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

// Naming the variable keeps the key out of the file, and the error for a key
// that is not set must name the variable the user chose.
func TestTheFileNameWhichVariableHoldsTheKey(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"OPENAI_BASE_URL": "https://example.test/v1",
		"MY_PROVIDER_KEY": "chosen-name-key",
	}), &File{Model: "m", APIKeyEnv: "MY_PROVIDER_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "chosen-name-key" {
		t.Fatal("the named variable was not read")
	}

	_, err = Load(env(map[string]string{
		"OPENAI_BASE_URL": "https://example.test/v1",
		"OPENAI_API_KEY":  "unused-default",
	}), &File{Model: "m", APIKeyEnv: "MY_PROVIDER_KEY"})
	if err == nil {
		t.Fatal("a named variable that is not set must be reported")
	}
	if !strings.Contains(err.Error(), "MY_PROVIDER_KEY") {
		t.Fatalf("the error must name the variable the file chose: %v", err)
	}
}

// A launcher that exports two different model aliases is a conflict worth
// reporting — unless the file settles the question, in which case the
// environment is no longer being asked.
func TestAFileModelSettlesAConflictingEnvironment(t *testing.T) {
	conflicting := env(map[string]string{
		"OPENAI_BASE_URL":   "https://example.test/v1",
		"OPENAI_API_KEY":    "key",
		"OPENAI_MODEL_NAME": "model-a",
		"OPENAI_MODEL":      "model-b",
	})
	if _, err := Load(conflicting, nil); err == nil {
		t.Fatal("the conflict must be reported when nothing else decides it")
	}
	cfg, err := Load(conflicting, &File{Model: "file-model"})
	if err != nil {
		t.Fatalf("a model stated in the file must settle it: %v", err)
	}
	if cfg.Model != "file-model" {
		t.Fatalf("model = %q", cfg.Model)
	}
}

func TestAMissingFileIsNotAnError(t *testing.T) {
	file, found, err := LoadFile(filepath.Join(t.TempDir(), FileName))
	if err != nil || found {
		t.Fatalf("found=%v err=%v; a file that is not there is not a problem", found, err)
	}
	if !isEmptyFile(file) {
		t.Fatalf("file = %#v, want nothing", file)
	}
}

func TestAnEmptyFileIsAnEmptyConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := LoadFile(path)
	if err != nil || !found || !isEmptyFile(file) {
		t.Fatalf("found=%v err=%v file=%#v", found, err, file)
	}
}

// A misspelled key that is ignored would leave the user with no effect and no
// reason, which is the one outcome a configuration file must not produce.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("modell: demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadFile(path); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

func TestAFileIsReadWithItsValuesTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := "model: \"  spaced-model  \"\nbase_url: \" https://example.test/v1 \"\nreasoning_effort: \" High \"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := LoadFile(path)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	cfg, err := Load(env(map[string]string{"OPENAI_API_KEY": "k"}), &file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "spaced-model" || cfg.ReasoningEffort != "high" || cfg.ProviderHost != "example.test" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

// An error about a configuration file is read by someone who knows where their
// own files are; the host's directory layout does not belong in it.
func TestAFileErrorNamesTheFileNameOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("model: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("the error leaks the absolute path: %v", err)
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Fatalf("the error does not name the file: %v", err)
	}
}

// A user who configured nothing has one model: the one their settings already
// resolve to. The list is never empty, because /model has to have something to
// switch back to.
func TestTheDefaultModelIsTheWholeListWithoutAFile(t *testing.T) {
	cfg, err := Load(fileEnv(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("models = %+v", cfg.Models)
	}
	only := cfg.Models[0]
	if only.Name != "env-model" || only.BaseURL != "https://env.example.test/v1" ||
		only.Provider != "env.example.test" || only.APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("the default entry is not the effective configuration: %+v", only)
	}
}

// Entries the file adds come after the default, and inherit what they leave out.
// A user with one provider and several models writes only names.
func TestFileModelsInheritTheEndpointAndKeyVariable(t *testing.T) {
	cfg, err := Load(fileEnv(), &File{
		Models: []Model{
			{Name: "second"},
			{Name: "third", BaseURL: "https://other.example.test/v1", APIKeyEnv: "OTHER_KEY", Provider: "Other"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 3 {
		t.Fatalf("models = %+v", cfg.Models)
	}
	if cfg.Models[0].Name != "env-model" {
		t.Fatalf("the default must come first: %+v", cfg.Models)
	}
	inherited := cfg.Models[1]
	if inherited.Name != "second" || inherited.BaseURL != "https://env.example.test/v1" ||
		inherited.APIKeyEnv != "OPENAI_API_KEY" || inherited.Provider != "env.example.test" {
		t.Fatalf("the second entry did not inherit: %+v", inherited)
	}
	own := cfg.Models[2]
	if own.BaseURL != "https://other.example.test/v1" || own.APIKeyEnv != "OTHER_KEY" || own.Provider != "Other" {
		t.Fatalf("the third entry did not keep its own values: %+v", own)
	}
}

// A name is how a run says which model it wants, so a name that matches twice
// cannot be chosen — and the user would only discover that by picking the wrong
// one.
func TestADuplicateModelNameIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		file  *File
		model string
	}{
		{"twice in the file", &File{Models: []Model{{Name: "a"}, {Name: "a"}}}, ""},
		{"once as the default", &File{Model: "env-model", Models: []Model{{Name: "env-model"}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(fileEnv(), tc.file); err == nil {
				t.Fatal("the duplicate name was accepted")
			}
		})
	}
}

func TestModelEntriesThatCannotBeUsedAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		entry Model
	}{
		{"no name", Model{Provider: "P"}},
		{"relative base url", Model{Name: "a", BaseURL: "not-a-url"}},
		{"unsupported scheme", Model{Name: "a", BaseURL: "ftp://example.test/v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(fileEnv(), &File{Models: []Model{tc.entry}}); err == nil {
				t.Fatalf("%+v was accepted", tc.entry)
			}
		})
	}
}
