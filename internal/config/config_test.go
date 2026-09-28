package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isEmptyFile reports whether a file carries nothing, which is what a missing or
// an empty file must produce.
func isEmptyFile(f File) bool {
	return f.ReasoningEffort == "" && f.MaxIterations == 0 && f.RunTimeout == ""
}

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

// providerValues is what the settings page would have written: one named
// provider with an endpoint, a key and a model.
func providerValues() ProviderValues {
	return ProviderValues{Name: "deepseek", BaseURL: "https://example.test/v1", APIKey: "test-key", Model: "test-model"}
}

func TestTheActiveProviderIsTheSource(t *testing.T) {
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
//
// With no provider at all the one thing to fill in is a provider: naming a
// base_url would point the interface at a field of an entry that does not exist.
func TestNoProviderAtAllNamesTheProvider(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, ProviderValues{})
	if err != nil {
		t.Fatalf("an unconfigured Luna must start: %v", err)
	}
	want := []string{"provider"}
	if strings.Join(cfg.Missing, ",") != strings.Join(want, ",") {
		t.Fatalf("missing = %v, want %v", cfg.Missing, want)
	}
	if len(cfg.Models) != 0 {
		t.Fatalf("models = %+v, want none while no provider is active", cfg.Models)
	}
	if cfg.Model != "" || cfg.BaseURL != "" || cfg.APIKey != "" {
		t.Fatalf("a Luna with no provider reports provider fields: %#v", cfg)
	}
}

func TestAPartlyConfiguredProviderNamesWhatIsStillMissing(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, ProviderValues{Name: "deepseek", BaseURL: "https://example.test/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Missing) != 1 || cfg.Missing[0] != "model" {
		t.Fatalf("missing = %v, want just the model", cfg.Missing)
	}
}

// A key that is set but unusable is the one thing an error must never quote.
func TestTheKeyNeverAppearsInAnError(t *testing.T) {
	_, err := Load(env(map[string]string{}), nil, ProviderValues{
		Name: "deepseek", BaseURL: "not-a-url", APIKey: "top-secret-value", Model: "m",
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
			Name: "deepseek", BaseURL: base, APIKey: "k", Model: "m",
		}); err == nil {
			t.Fatalf("%q was accepted", base)
		}
	}
}

// The hand-written file answers the questions about how a run is made. It has no
// vote on which provider that run is sent to.
func TestTheFileStatesHowARunIsMadeAndNotWhereItGoes(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), &File{ReasoningEffort: "high", MaxIterations: 7, RunTimeout: "90s"}, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "high" || cfg.MaxIterations != 7 || cfg.RunTimeout != 90*time.Second {
		t.Fatalf("the file's choices were not applied: %#v", cfg)
	}
	if cfg.Model != "test-model" || cfg.ProviderHost != "example.test" {
		t.Fatalf("the provider did not come from the active entry: %#v", cfg)
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
// reason, which is the one outcome a configuration file must not produce. The
// provider keys are refused here too: this file does not state an endpoint.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	for _, body := range []string{"modell: demo\n", "model: demo\n", "base_url: https://example.test/v1\n", "api_key_env: SOMETHING\n"} {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadFile(path); err == nil {
			t.Fatalf("%q was accepted", body)
		}
	}
}

func TestAFileIsReadWithItsValuesTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := "reasoning_effort: \" High \"\nrun_timeout: \" 90s \"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := LoadFile(path)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	cfg, err := Load(env(map[string]string{}), &file, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "high" || cfg.RunTimeout != 90*time.Second || cfg.ProviderHost != "example.test" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

// An error about a configuration file is read by someone who knows where their
// own files are; the host's directory layout does not belong in it.
func TestAFileErrorNamesTheFileNameOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("reasoning_effort: [unclosed\n"), 0o600); err != nil {
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

// A user who configured one model has one model: the provider's own. The list is
// never empty while a provider is active, because /model has to have something to
// switch back to.
func TestTheProvidersOwnModelIsTheWholeListWithNoExtras(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, providerValues())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("models = %+v", cfg.Models)
	}
	only := cfg.Models[0]
	if only.Name != "test-model" || only.Provider != "deepseek" {
		t.Fatalf("the default entry is not the active provider's own model: %+v", only)
	}
}

// The extras a provider lists come after its own model, in the order it lists
// them, and every entry says which provider it belongs to — that name is the
// label the interface shows.
func TestExtraModelsFollowTheProvidersOwnAndCarryTheProviderName(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, ProviderValues{
		Name: "deepseek", BaseURL: "https://example.test/v1", APIKey: "k", Model: "first",
		Models: []string{"second", " third "},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third"}
	if len(cfg.Models) != len(want) {
		t.Fatalf("models = %+v", cfg.Models)
	}
	for i, name := range want {
		if cfg.Models[i].Name != name || cfg.Models[i].Provider != "deepseek" {
			t.Fatalf("models[%d] = %+v, want %q from the active provider", i, cfg.Models[i], name)
		}
	}
}

// A name is how /model says which model it means, so a name that appears twice
// cannot be chosen. The file itself refuses a repeated name; this is the assembly
// step, which drops a repeat rather than offering the same choice twice.
func TestARepeatedModelNameIsListedOnce(t *testing.T) {
	cfg, err := Load(env(map[string]string{}), nil, ProviderValues{
		Name: "deepseek", BaseURL: "https://example.test/v1", APIKey: "k", Model: "first",
		Models: []string{"first", "second", "second", "  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 2 || cfg.Models[0].Name != "first" || cfg.Models[1].Name != "second" {
		t.Fatalf("models = %+v, want the two distinct names in order", cfg.Models)
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
