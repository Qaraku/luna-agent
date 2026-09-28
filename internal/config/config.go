package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	ProviderHost string
	// ReasoningEffort is how hard the model should think before it answers, sent
	// as the API's own reasoning_effort field. Empty means the field is not sent
	// at all, which is the default: a provider that has never heard of the field
	// must not be affected by a knob it did not ask for.
	ReasoningEffort string
	// Models is the set a run may be sent to, the default first. A user who has
	// configured none has exactly one entry: what the settings above resolve to.
	// Switching a run to another entry is what /model does.
	Models []Model
	// MaxIterations and RunTimeout are the two run budgets: how many model turns
	// one run may take, and how long it may take, before the layer that owns
	// that budget stops it. Zero means not stated, and then that layer applies
	// its own default — the number is declared once, where it is enforced,
	// rather than repeated here.
	MaxIterations int
	RunTimeout    time.Duration
	// Missing names the provider settings that are still unset, if any. A Luna
	// that has never been configured is a first run rather than a failure: the
	// settings page that fills this in is served by the same process, so
	// refusing to start would make the one thing that fixes it unreachable.
	// When it is not empty, the rest of the provider fields are zero.
	Missing []string
}

// ReasoningEffortEnv is where the level is read from. It is a separate variable
// from the ones that decide which provider is called, because the level is a
// choice about how a run is made, not about where it is sent.
const ReasoningEffortEnv = "LUNA_REASONING_EFFORT"

// ReasoningEffortLevels are the accepted levels, and they are the levels the
// chat-completions API defines for reasoning_effort. A level that sounds stronger
// but is not a value the API defines (max, ultra, xhigh) is not accepted: sending
// it would come back as a rejected request, not as more thinking.
var ReasoningEffortLevels = []string{"minimal", "low", "medium", "high", "none"}

// ParseReasoningEffort accepts the declared levels and the empty value, and
// reports anything else with the list of what would have worked.
func ParseReasoningEffort(value string) (string, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return "", nil
	}
	for _, level := range ReasoningEffortLevels {
		if trimmed == level {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("reasoning effort must be one of %s, or empty to send no reasoning_effort at all (set %s, or reasoning_effort in the user configuration file)",
		strings.Join(ReasoningEffortLevels, ", "), ReasoningEffortEnv)
}

// APIKeyEnv is the environment variable the key is read from when the
// configuration file names one and the provider file has no key of its own.
// It is empty by default: a Luna configured through the settings page has no
// use for a variable, and a default here would keep a second, invisible source
// of the same value alive.
const APIKeyEnv = ""

// MaxIterationsEnv and RunTimeoutEnv are where the two run budgets are read
// from when the configuration file states neither. They are separate variables
// from the ones that decide which provider is called, because how long a run
// may work is a choice about the run, not about where it is sent.
const (
	MaxIterationsEnv = "LUNA_MAX_ITERATIONS"
	RunTimeoutEnv    = "LUNA_RUN_TIMEOUT"
)

// ProviderValues are the provider settings the settings page owns: the endpoint,
// the key and the model. They are passed in as plain values rather than as the
// file type they come from, so this package does not depend on the package that
// owns that file.
//
// They are a source, not an override: whatever they state wins over the
// hand-written file, because they are what this installation was told most
// recently, and the settings page is where that happens.
type ProviderValues struct {
	BaseURL string
	APIKey  string
	Model   string
}

// ParseMaxIterations and ParseRunTimeout read one run budget each. The empty
// string means "not stated" and resolves to zero, which the layer that enforces
// the budget reads as "use your own default"; anything that is not a positive
// whole number (or a positive duration) is refused with the value it was given,
// because a budget silently read as zero is a budget that does not exist.
func ParseMaxIterations(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("max iterations must be a positive whole number (got %q); set %s or max_iterations in the user configuration file", trimmed, MaxIterationsEnv)
	}
	return n, nil
}

// ParseRunTimeout reads one run budget of time, written as a Go duration such
// as "20m" or "90s".
func ParseRunTimeout(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(trimmed)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("run timeout must be a positive duration such as 20m or 90s (got %q); set %s or run_timeout in the user configuration file", trimmed, RunTimeoutEnv)
	}
	return d, nil
}

// Load resolves the configuration from the files that state it: the provider
// file the settings page writes, and the user's hand-written configuration file.
// The environment still decides the two run budgets and the reasoning level,
// which are choices about how a run is made rather than about who it calls.
//
// Where both files state a provider setting, the provider file wins: it is what
// this installation was told most recently, and the settings page is where that
// happens. The hand-written file fills in what the provider file leaves out, so
// an installation configured by hand keeps working.
//
// A provider that states nothing is not an error — see Config.Missing.
func Load(getenv func(string) string, file *File, prov ProviderValues) (Config, error) {
	base := strings.TrimSpace(prov.BaseURL)
	key := strings.TrimSpace(prov.APIKey)
	model := strings.TrimSpace(prov.Model)
	effort := strings.TrimSpace(getenv(ReasoningEffortEnv))
	maxIterations, err := ParseMaxIterations(getenv(MaxIterationsEnv))
	if err != nil {
		return Config{}, err
	}
	runTimeout, err := ParseRunTimeout(getenv(RunTimeoutEnv))
	if err != nil {
		return Config{}, err
	}
	keyEnv := ""

	if file != nil {
		stated := file.trimmed()
		if stated.MaxIterations != 0 {
			// The file is the more specific statement, and ParseMaxIterations
			// is what decides whether a number is usable, so the file's value
			// goes through the same reading.
			parsed, err := ParseMaxIterations(strconv.Itoa(stated.MaxIterations))
			if err != nil {
				return Config{}, fmt.Errorf("max_iterations in the user configuration file: %w", err)
			}
			maxIterations = parsed
		}
		if stated.RunTimeout != "" {
			parsed, err := ParseRunTimeout(stated.RunTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("run_timeout in the user configuration file: %w", err)
			}
			runTimeout = parsed
		}
		if stated.ReasoningEffort != "" {
			effort = stated.ReasoningEffort
		}
		if base == "" {
			base = stated.BaseURL
		}
		if model == "" {
			model = stated.Model
		}
		if stated.APIKeyEnv != "" {
			// The file names the variable, never the key itself. This is the
			// remaining path for an installation configured entirely by hand;
			// it is consulted only when the settings page has no key to offer.
			keyEnv = stated.APIKeyEnv
			if key == "" {
				key = strings.TrimSpace(getenv(keyEnv))
			}
		}
	}
	level, err := ParseReasoningEffort(effort)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{BaseURL: base, APIKey: key, Model: model, ReasoningEffort: level, MaxIterations: maxIterations, RunTimeout: runTimeout}
	var missing []string
	if base == "" {
		missing = append(missing, "base_url")
	}
	if key == "" {
		missing = append(missing, "api_key")
	}
	if model == "" {
		missing = append(missing, "model")
	}
	if len(missing) > 0 {
		cfg.Missing = missing
		return cfg, nil
	}

	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Config{}, fmt.Errorf("the provider base URL must be an absolute http(s) URL (the settings page writes provider.yaml; base_url there is the endpoint)")
	}
	models, err := modelList(model, base, keyEnv, u.Hostname(), file)
	if err != nil {
		return Config{}, err
	}
	cfg.ProviderHost = u.Hostname()
	cfg.Models = models
	return cfg, nil
}

// modelList is the effective set of models a run may be sent to: the
// configuration's own default first, then every entry the file adds, each with
// the endpoint and key variable it inherits when it states none of its own.
//
// Two entries cannot share a name. A name is how a run says which model it
// wants, so a name that matches twice is a name that cannot be chosen — and the
// user would only find that out by picking the wrong one.
func modelList(defaultModel, base, keyEnv, defaultHost string, file *File) ([]Model, error) {
	models := []Model{{Name: defaultModel, Provider: defaultHost, BaseURL: base, APIKeyEnv: keyEnv}}
	if file == nil {
		return models, nil
	}
	seen := map[string]bool{defaultModel: true}
	for _, entry := range file.trimmed().Models {
		if entry.Name == "" {
			return nil, fmt.Errorf("a model entry in the user configuration file has no name")
		}
		if seen[entry.Name] {
			return nil, fmt.Errorf("the model %q is listed twice; /model chooses a model by name", entry.Name)
		}
		seen[entry.Name] = true
		if entry.BaseURL == "" {
			entry.BaseURL = base
		}
		if entry.APIKeyEnv == "" {
			entry.APIKeyEnv = keyEnv
		}
		u, err := url.Parse(entry.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, fmt.Errorf("the model %q must have an absolute http(s) base URL", entry.Name)
		}
		if entry.Provider == "" {
			entry.Provider = u.Hostname()
		}
		models = append(models, entry)
	}
	return models, nil
}

// modelFromEnv read the model from whichever alias was set. It is gone with the
// environment variables it read: a model comes from the provider file, or from
// the hand-written configuration file, and never from a variable that may or may
// not have been exported by whatever launched the process.
