package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/runconfig"
	"github.com/cloudwego/eino/adk"
)

func (r *Runner) selectedEntries() []plugin.Entry {
	out := make([]plugin.Entry, 0)
	if r.capabilities != nil {
		for _, entry := range r.capabilities.Enabled() {
			if r.selection.AllowsCapability(entry.Descriptor.ID) {
				out = append(out, entry)
			}
		}
	}
	return out
}
func entryNames(entries []plugin.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Descriptor.ID)
	}
	return out
}

type preparedAgent struct {
	runner   *adk.Runner
	entries  []plugin.Entry
	snapshot *runconfig.Snapshot
}

func (p *preparedAgent) freezeResources(ctx context.Context) (context.Context, error) {
	resources := make(map[string][]string)
	info, _ := plugin.Run(ctx)
	for _, entry := range p.entries {
		provider, ok := entry.Plugin.(plugin.RunResourceProvider)
		if !ok {
			continue
		}
		names, err := provider.RunResources(ctx)
		if err != nil {
			return ctx, fmt.Errorf("capability %s resources: %w", entry.Descriptor.ID, err)
		}
		selected := make([]string, 0, len(names))
		for _, name := range names {
			if info.Selection.AllowsResource(entry.Descriptor.ID, name) {
				selected = append(selected, name)
			}
		}
		resources[entry.Descriptor.ID] = selected
	}
	info.Resources = resources
	p.snapshot.Resources = resources
	return plugin.WithRun(ctx, info), nil
}

func selectionSignature(s *runconfig.Selection) string {
	if s == nil {
		return ""
	}
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
