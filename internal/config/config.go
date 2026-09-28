package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// ProviderHost is the endpoint's host, which is what a run is actually sent
	// to. It is reported so the interface can say where a run goes without
	// revealing a key.
	ProviderHost string
	// ReasoningEffort is how hard the model should think before it answers, sent
	// as the API's own reasoning_effort field. Empty means the field is not sent
	// at all, which is the default: a provider that has never heard of the field
	// must not be affected by a knob it did not ask for.
	ReasoningEffort string
	// Models is the set a run may be sent to, the active provider's default
	// first. A provider that names no extra model has exactly one entry, and a
	// Luna with no active provider has none. Switching a run to another entry is
	// what /model does.
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
	//
	// The names are provider.yaml's own keys, because they are what the
	// interface points at: "provider" when no provider is active, and otherwise
	// whichever of base_url, api_key and model the active one leaves out.
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

// MaxIterationsEnv and RunTimeoutEnv are where the two run budgets are read
// from when the configuration file states neither. They are separate variables
// from the ones that decide which provider is called, because how long a run
// may work is a choice about the run, not about where it is sent.
const (
	MaxIterationsEnv = "LUNA_MAX_ITERATIONS"
	RunTimeoutEnv    = "LUNA_RUN_TIMEOUT"
)

// ProviderValues are the settings the provider file holds for the active
// provider: its name, its endpoint, its key and its model list. They are passed
// in as plain values rather than as the file type they come from, so this
// package does not depend on the package that owns that file.
//
// They are the whole source of a provider: the hand-written configuration file
// states none of them, because which endpoint a run calls is what the settings
// page is for.
type ProviderValues struct {
	// Name is what provider.yaml calls this provider. It is the name the
	// interface shows and the one a missing field is reported against.
	Name string
	// BaseURL and APIKey are the endpoint and the secret every run of this
	// installation is sent with.
	BaseURL string
	APIKey  string
	// Model is the model a run that names none is sent to; Models are the other
	// ones the interface may offer.
	Model  string
	Models []string
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

// Load resolves the configuration from the two files that state it: the active
// provider the provider file holds, and the user's hand-written configuration
// file. The environment still decides the two run budgets and the reasoning
// level, which are choices about how a run is made rather than about who it
// calls.
//
// The provider is passed in already resolved, because which entry in that file
// is active is that file's business: this package does not know that file's
// shape, and the composition root that does resolves the active entry before
// calling here. Everything else about a run — the endpoint, the key, the model
// list — comes from those values alone; the configuration file cannot state a
// second endpoint, because two answers to "which provider" is one too many.
//
// A provider that states nothing is not an error — see Config.Missing.
func Load(getenv func(string) string, file *File, prov ProviderValues) (Config, error) {
	name := strings.TrimSpace(prov.Name)
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
	}
	level, err := ParseReasoningEffort(effort)
	if err != nil {
		return Config{}, err
	}

	// The budgets are resolved before the provider is looked at: they are
	// choices about how a run is made, and a Luna with no provider yet still
	// reports the budgets it would run under.
	cfg := Config{ReasoningEffort: level, MaxIterations: maxIterations, RunTimeout: runTimeout}
	if name == "" {
		// No provider is active, so nothing about a provider can be reported —
		// not even which of its fields is unset, because there is no provider
		// those fields would belong to. The one thing to fill in is a provider.
		cfg.Missing = []string{"provider"}
		return cfg, nil
	}
	cfg.BaseURL, cfg.APIKey, cfg.Model = base, key, model
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
	cfg.ProviderHost = u.Hostname()
	cfg.Models = modelList(name, model, prov.Models)
	return cfg, nil
}

// modelList is the effective set of models a run may be sent to: the active
// provider's own model first, then every extra name it lists, in the order the
// interface shows them. Every entry names the provider it belongs to, because
// that is the label the interface displays.
//
// Two entries cannot share a name. A name is how a run says which model it
// wants, so a name that matches twice is a name that cannot be chosen — and the
// user would only find that out by picking the wrong one. A duplicate is skipped
// here rather than refused: the file itself already refuses a repeated name, and
// what this function assembles is a list for an interface to render.
func modelList(providerName, defaultModel string, extra []string) []Model {
	models := []Model{{Name: defaultModel, Provider: providerName}}
	seen := map[string]bool{defaultModel: true}
	for _, name := range extra {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, Model{Name: name, Provider: providerName})
	}
	return models
}
