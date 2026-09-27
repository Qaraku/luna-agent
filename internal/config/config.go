package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
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
// configuration file does not name another one.
const APIKeyEnv = "OPENAI_API_KEY"

// ModelEnvNames are the environment variables a model may be named by, tried in
// this order. They are aliases of one another, not a precedence chain: two that
// disagree are refused, because which one a launcher meant is not something this
// package can know.
var ModelEnvNames = []string{"OPENAI_MODEL_NAME", "OPENAI_MODEL", "OPENAI_MODEL_ID"}

// Load resolves the configuration from its two sources: the environment, and
// the user's configuration file. file may be nil, and then this is the
// environment alone, which is how Luna has always been configured.
//
// Where the file states a value it wins, and the environment fills whatever it
// leaves out. The order is that way round because the file is the more specific
// statement: it is written for this user, while the environment may come from a
// shell profile or a launcher shared with other tools.
func Load(getenv func(string) string, file *File) (Config, error) {
	base := strings.TrimSpace(getenv("OPENAI_BASE_URL"))
	keyEnv := APIKeyEnv
	key := strings.TrimSpace(getenv(keyEnv))
	effort := strings.TrimSpace(getenv(ReasoningEffortEnv))
	model, _, modelErr := modelFromEnv(getenv)
	modelSource := strings.Join(ModelEnvNames, "/")

	if file != nil {
		stated := file.trimmed()
		if stated.BaseURL != "" {
			base = stated.BaseURL
		}
		if stated.ReasoningEffort != "" {
			effort = stated.ReasoningEffort
		}
		if stated.APIKeyEnv != "" {
			// The file names the variable, never the key itself; reading it is
			// the same lookup the default goes through.
			keyEnv = stated.APIKeyEnv
			key = strings.TrimSpace(getenv(keyEnv))
		}
		if stated.Model != "" {
			// A model stated by name settles it: the environment's aliases are
			// no longer being asked.
			model, modelErr = stated.Model, nil
			modelSource = "model in the configuration file"
		}
	}
	if modelErr != nil {
		return Config{}, modelErr
	}

	var missing []string
	if base == "" {
		missing = append(missing, "OPENAI_BASE_URL")
	}
	if key == "" {
		missing = append(missing, keyEnv)
	}
	if model == "" {
		missing = append(missing, modelSource)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %s (set it in the environment, or in the user configuration file)", strings.Join(missing, ", "))
	}

	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Config{}, fmt.Errorf("the provider base URL must be an absolute http(s) URL (set OPENAI_BASE_URL, or base_url in the user configuration file)")
	}
	level, err := ParseReasoningEffort(effort)
	if err != nil {
		return Config{}, err
	}
	return Config{BaseURL: base, APIKey: key, Model: model, ProviderHost: u.Hostname(), ReasoningEffort: level}, nil
}

// modelFromEnv reads the model from whichever alias is set. The second return
// value names the aliases that were set, for a caller that has to say what it
// looked for.
func modelFromEnv(getenv func(string) string) (string, []string, error) {
	values := map[string]string{}
	for _, name := range ModelEnvNames {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			values[name] = value
		}
	}
	var model string
	var used []string
	for _, name := range ModelEnvNames {
		value, ok := values[name]
		if !ok {
			continue
		}
		used = append(used, name)
		if model == "" {
			model = value
		} else if model != value {
			sort.Strings(used)
			return "", nil, fmt.Errorf("conflicting model environment variables: %s", strings.Join(used, ", "))
		}
	}
	return model, used, nil
}
