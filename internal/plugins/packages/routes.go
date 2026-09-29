package packages

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"unicode/utf8"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

const ManagerID = "packages"
const ManagerPrefix = "/api/packages"

//go:embed panel.js
var managerPanel []byte

//go:embed panel.css
var managerStyle []byte

func ManagerDescriptor() plugin.Descriptor {
	d := plugin.Descriptor{ID: ManagerID, Title: "插件包管理", Deployment: plugin.DeploymentBuiltin, Claims: []plugin.Claim{{Kind: plugin.ClaimStateNamespace, ID: ManagerID}, {Kind: plugin.ClaimRoutePrefix, ID: ManagerPrefix}}, Permissions: []plugin.Permission{{Kind: plugin.PermissionStateWrite}, {Kind: plugin.PermissionProcessExec}, {Kind: plugin.PermissionNetworkFetch}}}
	for _, path := range []string{ManagerPrefix, ManagerPrefix + "/install", ManagerPrefix + "/activate", ManagerPrefix + "/disable", ManagerPrefix + "/remove", ManagerPrefix + "/detail", ManagerPrefix + "/file", ManagerPrefix + "/panel.js", ManagerPrefix + "/panel.css"} {
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionRoute, ID: path})
	}
	d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionPanel, ID: ManagerID})
	d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimPanel, ID: ManagerID})
	return d
}
func (*Manager) Descriptor() plugin.Descriptor { return ManagerDescriptor() }

type managerRoute struct {
	method, path string
	serve        http.HandlerFunc
}

func (r managerRoute) Method() string                                   { return r.method }
func (r managerRoute) Path() string                                     { return r.path }
func (r managerRoute) ServeHTTP(w http.ResponseWriter, q *http.Request) { r.serve(w, q) }
func (*Manager) Panels() []plugin.Panel {
	return []plugin.Panel{{ID: ManagerID, Title: "插件包管理", Entry: ManagerPrefix + "/panel.js"}}
}

func (m *Manager) Routes() []plugin.Route {
	return []plugin.Route{
		managerRoute{http.MethodGet, ManagerPrefix, m.listHTTP}, managerRoute{http.MethodPost, ManagerPrefix + "/install", m.installHTTP}, managerRoute{http.MethodPost, ManagerPrefix + "/activate", m.activateHTTP}, managerRoute{http.MethodPost, ManagerPrefix + "/disable", m.disableHTTP}, managerRoute{http.MethodPost, ManagerPrefix + "/remove", m.removeHTTP}, managerRoute{http.MethodGet, ManagerPrefix + "/detail", m.detailHTTP}, managerRoute{http.MethodGet, ManagerPrefix + "/file", m.fileHTTP},
		managerRoute{http.MethodGet, ManagerPrefix + "/panel.js", managerAsset("text/javascript; charset=utf-8", managerPanel)}, managerRoute{http.MethodGet, ManagerPrefix + "/panel.css", managerAsset("text/css; charset=utf-8", managerStyle)},
	}
}
func packageResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func packageFailure(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, ErrPackageConflict) {
		status = 409
	}
	if errors.Is(err, ErrPackageNotFound) {
		status = 404
	}
	packageResponse(w, status, map[string]string{"error": err.Error()})
}
func packageDecode(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxManifestBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		packageFailure(w, err)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		packageFailure(w, fmt.Errorf("expected one request object"))
		return false
	}
	return true
}
func (m *Manager) listHTTP(w http.ResponseWriter, r *http.Request) {
	packageResponse(w, 200, map[string]any{"packages": m.List()})
}
func (m *Manager) installHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source Source `json:"source"`
	}
	if !packageDecode(w, r, &in) {
		return
	}
	v, err := m.Install(r.Context(), in.Source)
	if err != nil {
		packageFailure(w, err)
		return
	}
	packageResponse(w, 200, map[string]any{"candidate": v, "packages": m.List()})
}
func (m *Manager) activateHTTP(w http.ResponseWriter, r *http.Request) {
	var in Activation
	if !packageDecode(w, r, &in) {
		return
	}
	if err := m.Activate(r.Context(), in); err != nil {
		packageFailure(w, err)
		return
	}
	m.listHTTP(w, r)
}

type stateRequest struct {
	ID       string `json:"id"`
	Expected string `json:"expected_current"`
	Confirm  bool   `json:"confirm"`
}

func (m *Manager) disableHTTP(w http.ResponseWriter, r *http.Request) {
	var in stateRequest
	if !packageDecode(w, r, &in) {
		return
	}
	if err := m.SetEnabled(in.ID, in.Expected, false); err != nil {
		packageFailure(w, err)
		return
	}
	m.listHTTP(w, r)
}
func (m *Manager) removeHTTP(w http.ResponseWriter, r *http.Request) {
	var in stateRequest
	if !packageDecode(w, r, &in) {
		return
	}
	if !in.Confirm {
		packageFailure(w, fmt.Errorf("confirm removal; code revisions and user state will be retained"))
		return
	}
	if err := m.Remove(in.ID, in.Expected); err != nil {
		packageFailure(w, err)
		return
	}
	m.listHTTP(w, r)
}
func (m *Manager) detailHTTP(w http.ResponseWriter, r *http.Request) {
	v, err := m.store.Version(r.URL.Query().Get("id"), r.URL.Query().Get("revision"))
	if err != nil {
		packageFailure(w, err)
		return
	}
	packageResponse(w, 200, v)
}
func (m *Manager) fileHTTP(w http.ResponseWriter, r *http.Request) {
	v, err := m.store.Version(r.URL.Query().Get("id"), r.URL.Query().Get("revision"))
	if err != nil {
		packageFailure(w, err)
		return
	}
	name := r.URL.Query().Get("file")
	var found *FileDigest
	for i := range v.Files {
		if v.Files[i].Path == name {
			found = &v.Files[i]
			break
		}
	}
	if found == nil {
		packageFailure(w, ErrPackageNotFound)
		return
	}
	if found.Bytes > 64*1024 {
		packageResponse(w, 200, map[string]any{"file": found, "preview_unavailable": "file exceeds the 64 KiB text preview limit"})
		return
	}
	root, err := os.OpenRoot(v.Root)
	if err != nil {
		packageFailure(w, err)
		return
	}
	defer root.Close()
	body, _, err := readPackageFile(r.Context(), root, name, 64*1024)
	if err != nil {
		packageFailure(w, err)
		return
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		packageResponse(w, 200, map[string]any{"file": found, "preview_unavailable": "binary file"})
		return
	}
	packageResponse(w, 200, map[string]any{"file": found, "text": string(body)})
}

func managerAsset(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}
