// Package presets 提供用户拥有的工作预设目录；预设只能组合资源，不能授予权限。
package presets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/runconfig"
)

const Prefix = "/api/presets"

type Plugin struct{ store *Store }

func New(stateDir string) (*Plugin, error) {
	s, err := Open(stateDir)
	if err != nil {
		return nil, err
	}
	return &Plugin{store: s}, nil
}
func Descriptor() plugin.Descriptor {
	d := plugin.Descriptor{ID: PluginID, Title: "工作预设", Deployment: plugin.DeploymentBuiltin, Claims: []plugin.Claim{{Kind: plugin.ClaimStateNamespace, ID: "presets"}, {Kind: plugin.ClaimRoutePrefix, ID: Prefix}}, Permissions: []plugin.Permission{{Kind: plugin.PermissionStateWrite}}}
	for _, path := range []string{Prefix, Prefix + "/save", Prefix + "/archive", Prefix + "/restore", Prefix + "/history", Prefix + "/export"} {
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionRoute, ID: path})
	}
	return d
}
func (*Plugin) Descriptor() plugin.Descriptor { return Descriptor() }
func (p *Plugin) ResolveSetup(id, rev string) (*runconfig.Selection, error) {
	e, err := p.store.Lookup(id, rev)
	if err != nil {
		return nil, err
	}
	return e.Selection.Clone(), nil
}
func (p *Plugin) Routes() []plugin.Route {
	return []plugin.Route{
		route{http.MethodGet, Prefix, p.list}, route{http.MethodPost, Prefix + "/save", p.save}, route{http.MethodPost, Prefix + "/archive", p.archive}, route{http.MethodPost, Prefix + "/restore", p.restore}, route{http.MethodGet, Prefix + "/history", p.history}, route{http.MethodGet, Prefix + "/export", p.export},
	}
}

type route struct {
	method, path string
	serve        http.HandlerFunc
}

func (r route) Method() string                                   { return r.method }
func (r route) Path() string                                     { return r.path }
func (r route) ServeHTTP(w http.ResponseWriter, q *http.Request) { r.serve(w, q) }
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, ErrNotFound):
		status = 404
	case errors.Is(err, ErrConflict), errors.Is(err, ErrArchived), errors.Is(err, ErrBuiltin):
		status = 409
	case errors.Is(err, ErrCorrupt):
		status = 500
	}
	respond(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, runconfig.MaxSelectionBytes+2048))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		problem(w, err)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		problem(w, fmt.Errorf("expected one JSON object"))
		return false
	}
	return true
}
func (p *Plugin) list(w http.ResponseWriter, r *http.Request) {
	entries, err := p.store.List(r.URL.Query().Get("archived") == "true")
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, 200, map[string]any{"presets": entries})
}
func (p *Plugin) save(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Definition *runconfig.Selection `json:"definition"`
		Expected   string               `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Definition == nil {
		problem(w, fmt.Errorf("definition is required"))
		return
	}
	e, err := p.store.Save(*in.Definition, in.Expected)
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, 200, e)
}
func (p *Plugin) archive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string `json:"id"`
		Expected string `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := p.store.Archive(in.ID, in.Expected)
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, 200, e)
}
func (p *Plugin) restore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
		Expected string `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := p.store.Restore(in.ID, in.Revision, in.Expected)
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, 200, e)
}
func (p *Plugin) history(w http.ResponseWriter, r *http.Request) {
	entries, err := p.store.History(r.URL.Query().Get("id"))
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, 200, map[string]any{"revisions": entries})
}
func (p *Plugin) export(w http.ResponseWriter, r *http.Request) {
	e, err := p.store.Lookup(r.URL.Query().Get("id"), r.URL.Query().Get("revision"))
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, 200, e.Selection)
}
