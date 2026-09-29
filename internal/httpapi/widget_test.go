package httpapi

import (
	"github.com/Qaraku/luna-agent/internal/plugin"
	"testing"
)

type widgetViewFixture struct{}

func (widgetViewFixture) Descriptor() plugin.Descriptor {
	return plugin.Descriptor{ID: "metrics", Title: "运行指标", Deployment: plugin.DeploymentBuiltin}
}
func (widgetViewFixture) Widgets() []plugin.Widget {
	return []plugin.Widget{{ID: "metrics", Title: "运行指标", Entry: "/api/metrics/widget.js"}}
}
func TestWidgetStateOnlyPublishesEnabledContribution(t *testing.T) {
	p := widgetViewFixture{}
	for _, state := range []plugin.State{plugin.StateRegistered, plugin.StateEnabled, plugin.StateDisabled, plugin.StateFailed} {
		entry := plugin.Entry{Plugin: p, Descriptor: p.Descriptor(), State: state}
		view := capabilityViewOf(entry)
		if state == plugin.StateEnabled {
			if len(view.Widgets) != 1 || view.Widgets[0].Entry != "/api/metrics/widget.js" {
				t.Fatalf("enabled widget: %+v", view.Widgets)
			}
		} else if len(view.Widgets) != 0 {
			t.Fatalf("state %s exposes widgets", state)
		}
	}
}
