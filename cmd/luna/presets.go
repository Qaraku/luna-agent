package main

import (
	"fmt"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/presets"
)

func registerPresets(registry *plugin.Registry, stateRoot string, enabled bool) error {
	dir, err := plugin.StateDirFor(presets.Descriptor(), stateRoot)
	if err != nil {
		return fmt.Errorf("resolve preset state: %w", err)
	}
	capability, err := presets.New(dir)
	if err != nil {
		return fmt.Errorf("open preset catalog: %w", err)
	}
	if err = registry.Register(capability); err != nil {
		return fmt.Errorf("register presets: %w", err)
	}
	if enabled {
		if err = registry.Enable(presets.PluginID); err != nil {
			return fmt.Errorf("enable presets: %w", err)
		}
	}
	return nil
}
