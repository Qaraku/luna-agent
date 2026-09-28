package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// providerValues is what the settings page would have written: an endpoint, a
// key and a model, and nothing else.
func providerValues() ProviderValues {
	return ProviderValues{BaseURL: "https://example.test/v1", APIKey: "test-key", Model: "test-model"}
}

func TestTheProviderFileIsTheSource(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://example.test/v1" || cfg.APIKey != "test-key" || cfg.Model != "test-model" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.ProviderHost != "example.test" {
		t.Fatalf("provider host = %q, want the endpoint's host", cfg.ProviderHost)
	}
	if len(cfg.Missing) != 0 {
		t.Fatalf("a complete provider still reports missing fields: %v", cfg.Missing)
	}
}

// A Luna that has never been configured must still start: the settings page that
// fills this in is served by this same process, so refusing to start would make
// the one thing that fixes it unreachable.
func TestAnUnconfiguredProviderIsNotAnError(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, ProviderValues{})
	if err != nil {
		t.Fatalf("an unconfigured Luna must start: %v", err)
	}
	want := []string{"base_url", "api_key", "model"}
	if strings.Join(cfg.Missing, ",") != strings.Join(want, ",") {
		t.Fatalf("missing = %v, want %v", cfg.Missing, want)
	}
	if len(cfg.Models) != 0 {
		t.Fatalf("models = %+v, want none while the provider is unset", cfg.Models)
	}
}

func TestAPartlyConfiguredProviderNamesWhatIsStillMissing(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, ProviderValues{BaseURL: "https://example.test/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Missing) != 1 || cfg.Missing[0] != "model" {
		t.Fatalf("missing = %v, want just the model", cfg.Missing)
	}
}

// The settings page is where a provider is set most recently, so it wins where
// the hand-written file also states something.
func TestTheProviderFileWinsOverTheHandWrittenFile(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), &File{
		Model:           "file-model",
		BaseURL:         "https://file.example.test/v1",
		ReasoningEffort: "high",
	}, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "test-model" || cfg.ProviderHost != "example.test" {
		t.Fatalf("the hand-written file won where the provider file stated a value: %#v", cfg)
	}
	// A run budget is not a provider setting: it is still the file's to state.
	if cfg.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want the file's level", cfg.ReasoningEffort)
	}
}

// An installation configured entirely by hand keeps working: the hand-written
// file fills in what the provider file leaves out.
func TestTheHandWrittenFileFillsInWhatTheProviderFileLeavesOut(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), &File{
		Model:   "file-model",
		BaseURL: "https://file.example.test/v1",
	}, ProviderValues{APIKey: "only-key"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "file-model" || cfg.ProviderHost != "file.example.test" || cfg.APIKey != "only-key" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

// Naming a variable keeps a key out of a hand-written file, and it is only
// consulted when the settings page has no key to offer.
func TestTheHandWrittenFileMayNameAVariableForTheKey(t *testing.T) {
	cfg, err := Load(env(map[string]string{"MY_PROVIDER_KEY": "chosen-name-key"}), &File{
		Model:     "m",
		BaseURL:   "https://example.test/v1",
		APIKeyEnv: "MY_PROVIDER_KEY",
	}, ProviderValues{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "chosen-name-key" {
		t.Fatalf("the named variable was not read: %#v", cfg)
	}
}

// A file that names a variable nobody set is not an error the server refuses to
// start on: it is a provider that is not configured yet, and the settings page
// is what fills it in.
func TestANamedVariableThatIsUnsetLeavesTheProviderUnconfigured(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), &File{
		Model:     "m",
		BaseURL:   "https://example.test/v1",
		APIKeyEnv: "MY_PROVIDER_KEY",
	}, ProviderValues{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Missing) != 1 || cfg.Missing[0] != "api_key" {
		t.Fatalf("missing = %v, want just the key", cfg.Missing)
	}
}

// A key set through the settings page is the one that is used, even when the
// hand-written file names a variable as well: two answers to "which key" is one
// too many, and the settings page is the more specific of the two.
func TestTheProviderKeyWinsOverANamedVariable(t *testing.T) {
	cfg, err := Load(env(map[string]string{"MY_PROVIDER_KEY": "var-key"}), &File{
		Model:     "m",
		BaseURL:   "https://example.test/v1",
		APIKeyEnv: "MY_PROVIDER_KEY",
	}, ProviderValues{APIKey: "settings-key"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "settings-key" {
		t.Fatalf("api key = %q, want the one the settings page holds", cfg.APIKey)
	}
}

// A key that is set but unusable is the one thing an error must never quote.
func TestTheKeyNeverAppearsInAnError(t *testing.T) {
	_, err := Load(env(map[string]string{}), nil, ProviderValues{
		BaseURL: "not-a-url", APIKey: "top-secret-value", Model: "m",
	})
	if err == nil {
		t.Fatal("a relative base URL was accepted")
	}
	if strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("the error leaked the key: %v", err)
	}
}

func TestLoadRejectsABaseURLThatCannotBeCalled(t *testing.T) {
	for _, base := range []string{"example.test/v1", "ftp://example.test/v1", "https:///v1"} {
		if _, err := Load(env(map[string]string{}), nil, ProviderValues{
			BaseURL: base, APIKey: "k", Model: "m",
		}); err == nil {
			t.Fatalf("%q was accepted", base)
		}
	}
}

func effortEnv(level string) func(string) string {
	return env(map[string]string{ReasoningEffortEnv: level})
}

func TestLoadCarriesTheChosenReasoningEffort(t *testing.T) {
	cfg, err := Load(effortEnv("  High "), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "high" {
		t.Fatalf("got %q, want the declared level lowercased", cfg.ReasoningEffort)
	}
}

func TestLoadSendsNoReasoningEffortByDefault(t *testing.T) {
	cfg, err := Load(effortEnv(""), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "" {
		t.Fatalf("got %q, want nothing chosen", cfg.ReasoningEffort)
	}
}

func TestLoadRejectsALevelTheAPIDoesNotDefine(t *testing.T) {
	for _, level := range []string{"ultra", "xhigh", "max"} {
		_, err := Load(effortEnv(level), nil, providerValues())
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
	cfg, err := Load(env(map[string]string{}), &file, ProviderValues{APIKey: "k"})
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

// A user who configured one model has one model: the one the provider file
// names. The list is never empty while a provider is configured, because /model
// has to have something to switch back to.
func TestTheDefaultModelIsTheWholeListWithoutAFile(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("models = %+v", cfg.Models)
	}
	only := cfg.Models[0]
	if only.Name != "test-model" || only.BaseURL != "https://example.test/v1" || only.Provider != "example.test" {
		t.Fatalf("the default entry is not the effective configuration: %+v", only)
	}
}

// Entries the file adds come after the default, and inherit what they leave out.
// A user with one provider and several models writes only names.
func TestFileModelsInheritTheEndpoint(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), &File{
		Models: []Model{
			{Name: "second"},
			{Name: "third", BaseURL: "https://other.example.test/v1", Provider: "Other"},
		},
	}, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 3 {
		t.Fatalf("models = %+v", cfg.Models)
	}
	if cfg.Models[0].Name != "test-model" {
		t.Fatalf("the default must come first: %+v", cfg.Models)
	}
	inherited := cfg.Models[1]
	if inherited.Name != "second" || inherited.BaseURL != "https://example.test/v1" || inherited.Provider != "example.test" {
		t.Fatalf("the second entry did not inherit: %+v", inherited)
	}
	own := cfg.Models[2]
	if own.BaseURL != "https://other.example.test/v1" || own.Provider != "Other" {
		t.Fatalf("the third entry did not keep its own values: %+v", own)
	}
}

// A name is how a run says which model it wants, so a name that matches twice
// cannot be chosen — and the user would only discover that by picking the wrong
// one.
func TestADuplicateModelNameIsRefused(t *testing.T) {
	cases := []struct {
		name string
		file *File
	}{
		{"twice in the file", &File{Models: []Model{{Name: "a"}, {Name: "a"}}}},
		{"once as the default", &File{Models: []Model{{Name: "test-model"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(env(map[string]string{}), tc.file, providerValues()); err == nil {
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
			if _, err := Load(env(map[string]string{}), &File{Models: []Model{tc.entry}}, providerValues()); err == nil {
				t.Fatalf("%+v was accepted", tc.entry)
			}
		})
	}
}

// budgetEnv is an environment whose two run budgets are the arguments.
func budgetEnv(maxIterations, runTimeout string) func(string) string {
	values := map[string]string{}
	if maxIterations != "" {
		values[MaxIterationsEnv] = maxIterations
	}
	if runTimeout != "" {
		values[RunTimeoutEnv] = runTimeout
	}
	return env(values)
}

// A run budget that is not stated is zero, which the layer that enforces it
// reads as "use your own default": the number is declared once, where it is
// enforced, rather than repeated in the configuration.
func TestUnstatedRunBudgetsResolveToZero(t *testing.T) {
	cfg, err := Load(budgetEnv("", ""), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxIterations != 0 || cfg.RunTimeout != 0 {
		t.Fatalf("unstated budgets became %d, %v", cfg.MaxIterations, cfg.RunTimeout)
	}
}

func TestTheEnvironmentCarriesBothRunBudgets(t *testing.T) {
	cfg, err := Load(budgetEnv("120", "25m"), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxIterations != 120 {
		t.Fatalf("max iterations = %d", cfg.MaxIterations)
	}
	if cfg.RunTimeout != 25*time.Minute {
		t.Fatalf("run timeout = %v", cfg.RunTimeout)
	}
}

func TestTheFileOverridesBothRunBudgets(t *testing.T) {
	cfg, err := Load(budgetEnv("120", "25m"), &File{MaxIterations: 8, RunTimeout: " 90s "}, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxIterations != 8 || cfg.RunTimeout != 90*time.Second {
		t.Fatalf("file budgets not applied: %d, %v", cfg.MaxIterations, cfg.RunTimeout)
	}
}

// A budget that cannot be used is refused rather than read as zero: a run that
// silently ignores the number its user set is a run whose bound nobody knows.
func TestRunBudgetsThatCannotBeUsedAreRefused(t *testing.T) {
	cases := []struct {
		name          string
		maxIterations string
		runTimeout    string
		file          *File
		mentions      string
	}{
		{"zero iterations", "0", "", nil, MaxIterationsEnv},
		{"negative iterations", "-3", "", nil, MaxIterationsEnv},
		{"not a number", "many", "", nil, MaxIterationsEnv},
		{"zero timeout", "", "0s", nil, RunTimeoutEnv},
		{"negative timeout", "", "-5m", nil, RunTimeoutEnv},
		{"not a duration", "", "soon", nil, RunTimeoutEnv},
		{"file iterations", "", "", &File{MaxIterations: -1}, "max_iterations"},
		{"file timeout", "", "", &File{RunTimeout: "later"}, "run_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(budgetEnv(tc.maxIterations, tc.runTimeout), tc.file, providerValues())
			if err == nil {
				t.Fatal("a budget that cannot be used was accepted")
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Fatalf("error %q does not name %q", err, tc.mentions)
			}
		})
	}
}
