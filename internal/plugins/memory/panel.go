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

// panelRoute serves that module. Only GET is declared, and the Kernel is what
// matches methods: this route is never called with anything else.
type panelRoute struct{}

func (panelRoute) Method() string { return http.MethodGet }
func (panelRoute) Path() string   { return PanelEntryPath }

func (panelRoute) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(panelModule)
}
