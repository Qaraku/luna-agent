package plugin

import (
	"net/http"
	"strings"
	"testing"
)

type widgetPlugin struct {
	d       Descriptor
	widgets []Widget
	routes  []Route
}

func (p widgetPlugin) Descriptor() Descriptor { return p.d }
func (p widgetPlugin) Widgets() []Widget      { return p.widgets }
func (p widgetPlugin) Routes() []Route        { return p.routes }

type widgetAsset struct{ path string }

func (r widgetAsset) Path() string                                 { return r.path }
func (r widgetAsset) Method() string                               { return http.MethodGet }
func (r widgetAsset) ServeHTTP(http.ResponseWriter, *http.Request) {}
func widgetFixture(id, widgetID string) widgetPlugin {
	entry := "/api/" + id + "/widget.js"
	return widgetPlugin{
		d: Descriptor{ID: id, Title: id, Deployment: DeploymentBuiltin,
			Contributions: []Contribution{{Kind: ContributionWidget, ID: widgetID}, {Kind: ContributionRoute, ID: entry}},
			Claims:        []Claim{{Kind: ClaimWidget, ID: widgetID}, {Kind: ClaimRoutePrefix, ID: "/api/" + id}}},
		widgets: []Widget{{ID: widgetID, Title: "运行指标", Entry: entry}}, routes: []Route{widgetAsset{entry}},
	}
}
func TestWidgetContributionLifecycleAndConflict(t *testing.T) {
	r := NewRegistry()
	p := widgetFixture("metrics", "metrics")
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("metrics"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(widgetFixture("other", "metrics")); err == nil {
		t.Fatal("duplicate widget accepted")
	}
	if err := r.Disable("metrics"); err != nil {
		t.Fatal(err)
	}
}
func TestWidgetDeclarationAndAssetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*widgetPlugin)
	}{
		{"missing exposure", func(p *widgetPlugin) { p.widgets = nil }},
		{"undeclared", func(p *widgetPlugin) { p.widgets[0].ID = "other" }},
		{"duplicate", func(p *widgetPlugin) { p.widgets = append(p.widgets, p.widgets[0]) }},
		{"foreign origin", func(p *widgetPlugin) { p.widgets[0].Entry = "https://other.invalid/widget.js" }},
		{"foreign route", func(p *widgetPlugin) { p.widgets[0].Entry = "/api/other/widget.js" }},
		{"unserved asset", func(p *widgetPlugin) { p.widgets[0].Entry = "/api/metrics/missing.js" }},
		{"encoded path", func(p *widgetPlugin) { p.widgets[0].Entry = "/api/metrics/%2e%2e/widget.js" }},
		{"no resource claim", func(p *widgetPlugin) { p.d.Claims = p.d.Claims[1:] }},
		{"blank title", func(p *widgetPlugin) { p.widgets[0].Title = " " }},
		{"invalid id", func(p *widgetPlugin) { p.widgets[0].ID = "../bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := widgetFixture("metrics", "metrics")
			tc.mutate(&p)
			if err := NewRegistry().Register(p); err == nil {
				t.Fatal("invalid widget accepted")
			}
		})
	}
	p := widgetFixture("metrics", "metrics")
	err := NewRegistry().Register(barePlugin{d: p.d})
	if err == nil || !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("missing implementation: %v", err)
	}
}

func TestWidgetSourceMustBeOwnedAndServed(t *testing.T) {
	p := widgetFixture("metrics", "metrics")
	p.widgets[0].Source = "/api/metrics/instances"
	if err := NewRegistry().Register(p); err == nil {
		t.Fatal("unserved source accepted")
	}
	p.d.Contributions = append(p.d.Contributions, Contribution{Kind: ContributionRoute, ID: p.widgets[0].Source})
	p.routes = append(p.routes, widgetAsset{p.widgets[0].Source})
	if err := NewRegistry().Register(p); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"/api/other/instances", "https://other.invalid/data", "/api/metrics/../other", "/api/metrics/\\other"} {
		p.widgets[0].Source = source
		if err := NewRegistry().Register(p); err == nil {
			t.Fatalf("source accepted: %q", source)
		}
	}
}
