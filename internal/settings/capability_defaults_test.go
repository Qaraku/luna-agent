package settings

import (
	"path/filepath"
	"testing"
)

func TestExplicitCapabilityDisableOverridesBuiltinDefaultAfterRestart(t *testing.T) {
	s := Settings{}
	if !s.CapabilityEnabledByDefault("presets", true) {
		t.Fatal("built-in default ignored")
	}
	s = s.WithCapabilityEnabled("presets", false).WithCapabilityEnabled("terminal", true)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CapabilityEnabledByDefault("presets", true) || !loaded.CapabilityEnabled("terminal") {
		t.Fatal("explicit preferences lost")
	}
	if !loaded.WithCapabilityEnabled("presets", true).CapabilityEnabledByDefault("presets", true) {
		t.Fatal("cannot re-enable")
	}
}
func TestCapabilityDenyListWinsOverConflictingEnable(t *testing.T) {
	s := Settings{Capabilities: Capabilities{Enabled: []string{"presets"}, Disabled: []string{"presets"}}}
	if s.CapabilityEnabled("presets") || s.CapabilityEnabledByDefault("presets", true) {
		t.Fatal("conflicting preference enabled a capability")
	}
}
