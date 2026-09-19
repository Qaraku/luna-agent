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
	return Config{BaseURL: base, APIKey: key, Model: model, ProviderHost: u.Hostname()}, nil
}
