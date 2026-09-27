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
	return "", fmt.Errorf("%s must be one of %s, or empty to send no reasoning_effort at all",
		ReasoningEffortEnv, strings.Join(ReasoningEffortLevels, ", "))
}

func Load(getenv func(string) string) (Config, error) {
	var missing []string
	base := strings.TrimSpace(getenv("OPENAI_BASE_URL"))
	key := strings.TrimSpace(getenv("OPENAI_API_KEY"))
	if base == "" {
		missing = append(missing, "OPENAI_BASE_URL")
	}
	if key == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}

	aliases := []string{"OPENAI_MODEL_NAME", "OPENAI_MODEL", "OPENAI_MODEL_ID"}
	values := map[string]string{}
	for _, name := range aliases {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			values[name] = value
		}
	}
	if len(values) == 0 {
		missing = append(missing, "OPENAI_MODEL_NAME/OPENAI_MODEL/OPENAI_MODEL_ID")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	var model string
	var used []string
	for _, name := range aliases {
		if value, ok := values[name]; ok {
			used = append(used, name)
			if model == "" {
				model = value
			} else if model != value {
				sort.Strings(used)
				return Config{}, fmt.Errorf("conflicting model environment variables: %s", strings.Join(used, ", "))
			}
		}
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Config{}, fmt.Errorf("OPENAI_BASE_URL must be an absolute http(s) URL")
	}
	effort, err := ParseReasoningEffort(getenv(ReasoningEffortEnv))
	if err != nil {
		return Config{}, err
	}
	return Config{BaseURL: base, APIKey: key, Model: model, ProviderHost: u.Hostname(), ReasoningEffort: effort}, nil
}
