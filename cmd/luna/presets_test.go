package main

import (
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/presets"
	"testing"
)

func TestPresetCompositionHonorsEnableChoice(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		reg := plugin.NewRegistry(plugin.PermissionStateWrite)
		if err := registerPresets(reg, t.TempDir(), enabled); err != nil {
			t.Fatal(err)
		}
		entry, ok := reg.Entry(presets.PluginID)
		if !ok || (entry.State == plugin.StateEnabled) != enabled {
			t.Fatalf("enabled=%v entry=%+v", enabled, entry)
		}
	}
}
