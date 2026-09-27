package memory

import (
	_ "embed"
	"net/http"
)

// panelModule is the capability's browser panel, served from the capability's
// own route. The module travels with the plugin rather than being handed to the
// host, so disabling the capability removes the panel and the code behind it
// together.
//
//go:embed panel.js
var panelModule []byte

// panelStylesheet is the panel's rules, served from the capability's own route
// like the module is. It is a file and not an injected <style> element because
// the service's CSP is `default-src 'self'`: a same-origin stylesheet is
// allowed, an inline style element is refused, and a refused one leaves the
// panel unstyled in a real service.
//
//go:embed panel.css
var panelStylesheet []byte

// panelRoute serves the panel module. Only GET is declared, and the Kernel is
// what matches methods: this route is never called with anything else.
type panelRoute struct{}

func (panelRoute) Method() string { return http.MethodGet }
func (panelRoute) Path() string   { return PanelEntryPath }

func (panelRoute) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	serveAsset(w, "text/javascript; charset=utf-8", panelModule)
}

// panelStyleRoute serves the stylesheet the module links. Same story as the
// module: it is the capability's asset, on the capability's prefix, so it goes
// out of service with the capability.
type panelStyleRoute struct{}

func (panelStyleRoute) Method() string { return http.MethodGet }
func (panelStyleRoute) Path() string   { return PanelStylePath }

func (panelStyleRoute) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	serveAsset(w, "text/css; charset=utf-8", panelStylesheet)
}

// serveAsset writes one embedded asset with its own content type. Both assets
// are nosniff: the browser must not guess a type for a body the capability
// serves as a fixed file.
func serveAsset(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
