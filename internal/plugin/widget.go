package plugin

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Widget 是能力提供的运行时组件。宿主拥有布局和可见性，模块只拥有容器内容。
// Entry 必须由同一个能力的已声明 GET 路由提供，不能指向外站或其他能力。
type Widget struct {
	ID, Title, Entry string
	Source           string
}
type WidgetProvider interface{ Widgets() []Widget }

var widgetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)

func validateWidgets(p Plugin, d Descriptor) error {
	declared := make(map[string]bool)
	for _, c := range d.Contributions {
		if c.Kind == ContributionWidget {
			declared[c.ID] = true
		}
	}
	provider, ok := p.(WidgetProvider)
	if !ok {
		if len(declared) > 0 {
			return fmt.Errorf("plugin %q declares widgets but does not implement WidgetProvider", d.ID)
		}
		return nil
	}
	exposed := make(map[string]bool)
	for _, widget := range provider.Widgets() {
		if !widgetIDPattern.MatchString(widget.ID) || strings.TrimSpace(widget.Title) == "" || utf8.RuneCountInString(widget.Title) > 80 {
			return fmt.Errorf("plugin %q has invalid widget metadata", d.ID)
		}
		if !declared[widget.ID] || exposed[widget.ID] {
			return fmt.Errorf("plugin %q has undeclared or duplicate widget %q", d.ID, widget.ID)
		}
		exposed[widget.ID] = true
		claimed := false
		for _, c := range d.Claims {
			if c.Kind == ClaimWidget && c.ID == widget.ID {
				claimed = true
			}
		}
		if !claimed {
			return fmt.Errorf("plugin %q widget %q has no widget claim", d.ID, widget.ID)
		}
		if err := validateWidgetEndpoint(p, d, widget.ID, widget.Entry); err != nil {
			return err
		}
		if widget.Source != "" {
			if err := validateWidgetEndpoint(p, d, widget.ID, widget.Source); err != nil {
				return err
			}
		}

	}
	return requireExposed(d, ContributionWidget, exposed, "widget")
}

func validateWidgetEndpoint(p Plugin, d Descriptor, id, entry string) error {
	owned := false
	for _, c := range d.Claims {
		if c.Kind == ClaimRoutePrefix && strings.HasPrefix(entry, c.ID+"/") {
			owned = true
		}
	}
	if !owned || !strings.HasPrefix(entry, "/api/") || path.Clean(entry) != entry || strings.ContainsAny(entry, `\%?#`) || strings.IndexFunc(entry, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return fmt.Errorf("plugin %q widget %q endpoint must stay in its own route prefix", d.ID, id)
	}
	if routes, ok := p.(RouteProvider); ok {
		for _, route := range routes.Routes() {
			if route.Method() == http.MethodGet && (route.Path() == entry || (strings.HasSuffix(route.Path(), "/") && strings.HasPrefix(entry, route.Path()))) {
				return nil
			}
		}
	}
	return fmt.Errorf("plugin %q widget %q endpoint has no GET route", d.ID, id)
}
