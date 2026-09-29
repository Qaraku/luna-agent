package skills

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

const LibraryPrefix = "/api/skill-library"
const LibraryPanelID = "skill-library"

//go:embed library_panel.js
var libraryPanelModule []byte

//go:embed library_panel.css
var libraryPanelStyle []byte

type libraryRoute struct {
	method, path string
	serve        http.HandlerFunc
}

func (r libraryRoute) Method() string                                   { return r.method }
func (r libraryRoute) Path() string                                     { return r.path }
func (r libraryRoute) ServeHTTP(w http.ResponseWriter, q *http.Request) { r.serve(w, q) }
func libraryPaths() []string {
	return []string{LibraryPrefix, LibraryPrefix + "/detail", LibraryPrefix + "/preview", LibraryPrefix + "/save", LibraryPrefix + "/restore", LibraryPrefix + "/history", LibraryPrefix + "/export", LibraryPrefix + "/panel.js", LibraryPrefix + "/panel.css"}
}
func (p *Plugin) Routes() []plugin.Route {
	if p.library == nil {
		return nil
	}
	return []plugin.Route{
		libraryRoute{http.MethodGet, LibraryPrefix, p.listLibrary}, libraryRoute{http.MethodGet, LibraryPrefix + "/detail", p.libraryDetail},
		libraryRoute{http.MethodPost, LibraryPrefix + "/preview", p.previewLibrary}, libraryRoute{http.MethodPost, LibraryPrefix + "/save", p.saveLibrary},
		libraryRoute{http.MethodPost, LibraryPrefix + "/restore", p.restoreLibrary}, libraryRoute{http.MethodGet, LibraryPrefix + "/history", p.libraryHistory}, libraryRoute{http.MethodGet, LibraryPrefix + "/export", p.exportLibrary},
		libraryRoute{http.MethodGet, LibraryPrefix + "/panel.js", libraryAsset("text/javascript; charset=utf-8", libraryPanelModule)},
		libraryRoute{http.MethodGet, LibraryPrefix + "/panel.css", libraryAsset("text/css; charset=utf-8", libraryPanelStyle)},
	}
}
func libraryResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func libraryProblem(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, ErrLibraryNotFound):
		status = 404
	case errors.Is(err, ErrLibraryConflict):
		status = 409
	case plugin.IsUnavailable(managedError(err)):
		status = 500
	}
	libraryResponse(w, status, map[string]string{"error": err.Error()})
}
func libraryDecode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxSkillBundleBytes*6+16*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		libraryProblem(w, err)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		libraryProblem(w, fmt.Errorf("expected one JSON object"))
		return false
	}
	return true
}
func (p *Plugin) listLibrary(w http.ResponseWriter, r *http.Request) {
	entries, err := p.library.List()
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, map[string]any{"entries": entries})
}
func (p *Plugin) libraryDetail(w http.ResponseWriter, r *http.Request) {
	e, err := p.library.Lookup(r.URL.Query().Get("name"), r.URL.Query().Get("revision"))
	if err != nil {
		libraryProblem(w, err)
		return
	}
	definition, err := p.library.Definition(e.Name, e.Revision)
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, map[string]any{"entry": e, "definition": definition})
}

type libraryWriteRequest struct {
	Definition *SkillDefinition `json:"definition"`
	Expected   string           `json:"expected_revision"`
	Reason     string           `json:"reason"`
}

func (p *Plugin) previewLibrary(w http.ResponseWriter, r *http.Request) {
	var in libraryWriteRequest
	if !libraryDecode(w, r, &in) {
		return
	}
	if in.Definition == nil {
		libraryProblem(w, fmt.Errorf("definition is required"))
		return
	}
	if err := p.checkLearnedName(in.Definition.Name); err != nil {
		libraryProblem(w, err)
		return
	}
	preview, err := p.library.Preview(*in.Definition, in.Expected)
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, preview)
}
func (p *Plugin) saveLibrary(w http.ResponseWriter, r *http.Request) {
	var in libraryWriteRequest
	if !libraryDecode(w, r, &in) {
		return
	}
	if in.Definition == nil {
		libraryProblem(w, fmt.Errorf("definition is required"))
		return
	}
	if err := p.checkLearnedName(in.Definition.Name); err != nil {
		libraryProblem(w, err)
		return
	}
	e, err := p.library.SaveContext(r.Context(), *in.Definition, in.Expected, SkillOrigin{Kind: "user", Reason: in.Reason})
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, e)
}
func (p *Plugin) restoreLibrary(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
		Expected string `json:"expected_revision"`
		Reason   string `json:"reason"`
	}
	if !libraryDecode(w, r, &in) {
		return
	}
	if in.Revision == "" || in.Expected == "" {
		libraryProblem(w, fmt.Errorf("revision and expected_revision are required"))
		return
	}
	e, err := p.library.Restore(in.Name, in.Revision, in.Expected, SkillOrigin{Kind: "user", Reason: in.Reason})
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, e)
}
func (p *Plugin) libraryHistory(w http.ResponseWriter, r *http.Request) {
	entries, err := p.library.History(r.URL.Query().Get("name"))
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, map[string]any{"revisions": entries})
}
func (p *Plugin) exportLibrary(w http.ResponseWriter, r *http.Request) {
	e, err := p.library.Lookup(r.URL.Query().Get("name"), r.URL.Query().Get("revision"))
	if err != nil {
		libraryProblem(w, err)
		return
	}
	definition, err := p.library.Definition(e.Name, e.Revision)
	if err != nil {
		libraryProblem(w, err)
		return
	}
	files, err := canonicalFiles(definition)
	if err != nil {
		libraryProblem(w, err)
		return
	}
	libraryResponse(w, 200, map[string]any{"name": e.Name, "revision": e.Revision, "files": files})
}

func libraryAsset(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}
func (p *Plugin) Panels() []plugin.Panel {
	if p.library == nil {
		return nil
	}
	return []plugin.Panel{{ID: LibraryPanelID, Title: "个人技能库", Entry: LibraryPrefix + "/panel.js"}}
}
