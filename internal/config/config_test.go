package config

import (
	"strings"
	"testing"
)

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
	}))
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
	}))
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
	_, err := Load(env(map[string]string{}))
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
	cfg, err := Load(effortEnv("  High "))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "high" {
		t.Fatalf("got %q, want the declared level lowercased", cfg.ReasoningEffort)
	}
}

func TestLoadSendsNoReasoningEffortByDefault(t *testing.T) {
	cfg, err := Load(effortEnv(""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReasoningEffort != "" {
		t.Fatalf("got %q, want nothing chosen", cfg.ReasoningEffort)
	}
}

func TestLoadRejectsALevelTheAPIDoesNotDefine(t *testing.T) {
	for _, level := range []string{"ultra", "xhigh", "max"} {
		_, err := Load(effortEnv(level))
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
