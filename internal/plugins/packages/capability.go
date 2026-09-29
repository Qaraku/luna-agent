package packages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/terminal"
	"github.com/Qaraku/luna-agent/internal/runconfig"
	jsonschema "github.com/eino-contrib/jsonschema"
)

type Capability struct {
	store      *Store
	version    Version
	stateDir   string
	setEnabled func(bool) error
}

func NewCapability(store *Store, version Version, stateDir string, setEnabled func(bool) error) (*Capability, error) {
	if err := version.Manifest.Validate(); err != nil {
		return nil, err
	}
	if store == nil || !filepath.IsAbs(version.Root) || !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("package capability requires verified roots")
	}
	needsState := false
	for _, tool := range version.Manifest.Tools {
		needsState = needsState || tool.Access.State != ""
	}
	if needsState {
		if err := os.MkdirAll(stateDir, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(stateDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("package state root must be a real directory")
		}
	}
	return &Capability{store: store, version: version, stateDir: stateDir, setEnabled: setEnabled}, nil
}
func (p *Capability) SetEnabled(enabled bool) error {
	if p.setEnabled == nil {
		return fmt.Errorf("use the package manager to review and change package state")
	}
	return p.setEnabled(enabled)
}
func (p *Capability) prefix() string {
	return "/api/" + p.version.Manifest.CapabilityID() + "/" + p.version.Revision
}
func (p *Capability) surfaceID(id string) string { return p.version.Manifest.CapabilityID() + "-" + id }
func (p *Capability) Descriptor() plugin.Descriptor {
	m := p.version.Manifest
	d := plugin.Descriptor{ID: m.CapabilityID(), Title: m.Title, Deployment: plugin.DeploymentBuiltin}
	if len(m.Tools) > 0 {
		d.Deployment = plugin.DeploymentProcess
	}
	permissions := map[plugin.PermissionKind]bool{}
	for _, tool := range m.Tools {
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionTool, ID: m.ToolName(tool.Name)})
		permissions[plugin.PermissionProcessExec] = true
		if tool.Access.Write {
			permissions[plugin.PermissionFilesystemWrite] = true
		}
		if tool.Access.Network {
			permissions[plugin.PermissionNetworkFetch] = true
		}
		if tool.Access.State == "write" {
			permissions[plugin.PermissionStateWrite] = true
		}
	}
	needsState := false
	for _, tool := range m.Tools {
		needsState = needsState || tool.Access.State != ""
	}
	if needsState {
		d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimStateNamespace, ID: m.CapabilityID()})
	}
	for _, kind := range []plugin.PermissionKind{plugin.PermissionProcessExec, plugin.PermissionFilesystemWrite, plugin.PermissionNetworkFetch, plugin.PermissionStateWrite} {
		if permissions[kind] {
			d.Permissions = append(d.Permissions, plugin.Permission{Kind: kind})
		}
	}
	if len(m.Static) > 0 {
		d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimRoutePrefix, ID: p.prefix()})
	}
	for _, file := range m.Static {
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionRoute, ID: p.prefix() + "/" + file})
	}
	for _, panel := range m.Panels {
		id := p.surfaceID(panel.ID)
		d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimPanel, ID: id})
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionPanel, ID: id})
	}
	for _, widget := range m.Widgets {
		id := p.surfaceID(widget.ID)
		d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimWidget, ID: id})
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionWidget, ID: id})
	}
	return d
}
func (p *Capability) Tools() []plugin.Tool {
	out := []plugin.Tool{}
	for _, spec := range p.version.Manifest.Tools {
		out = append(out, packageTool{p, spec})
	}
	return out
}
func (p *Capability) Panels() []plugin.Panel {
	out := []plugin.Panel{}
	for _, item := range p.version.Manifest.Panels {
		out = append(out, plugin.Panel{ID: p.surfaceID(item.ID), Title: item.Title, Entry: p.prefix() + "/" + item.Entry})
	}
	return out
}
func (p *Capability) Widgets() []plugin.Widget {
	out := []plugin.Widget{}
	for _, item := range p.version.Manifest.Widgets {
		w := plugin.Widget{ID: p.surfaceID(item.ID), Title: item.Title, Entry: p.prefix() + "/" + item.Entry}
		if item.Source != "" {
			w.Source = p.prefix() + "/" + item.Source
		}
		out = append(out, w)
	}
	return out
}

type assetRoute struct {
	owner *Capability
	file  string
}

func (r assetRoute) Method() string { return http.MethodGet }
func (r assetRoute) Path() string   { return r.owner.prefix() + "/" + r.file }
func (r assetRoute) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	root, err := os.OpenRoot(r.owner.version.Root)
	if err != nil {
		http.Error(w, "package asset unavailable", 500)
		return
	}
	defer root.Close()
	body, executable, err := readPackageFile(request.Context(), root, r.file, MaxPackageFileBytes)
	if err != nil || !r.owner.fileMatches(r.file, body, executable) {
		http.Error(w, "package asset failed integrity checks", 500)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(r.file))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
func (p *Capability) Routes() []plugin.Route {
	out := []plugin.Route{}
	for _, file := range p.version.Manifest.Static {
		out = append(out, assetRoute{p, file})
	}
	return out
}
func (p *Capability) fileMatches(name string, body []byte, executable bool) bool {
	for _, file := range p.version.Files {
		if file.Path == name {
			return file.Hash == digest(body) && file.Bytes == int64(len(body)) && file.Executable == executable
		}
	}
	return false
}
func (p *Capability) Setups() ([]runconfig.Selection, error) {
	out := []runconfig.Selection{}
	for _, original := range p.version.Manifest.Presets {
		selection := *original.Clone()
		selection.Owner = p.version.Manifest.CapabilityID()
		selection.ID = p.version.Manifest.SkillName(original.ID)
		selection.Revision = p.version.Revision
		out = append(out, selection)
	}
	return out, nil
}
func (p *Capability) ResolveSetup(id, revision string) (*runconfig.Selection, error) {
	if revision != "" && revision != p.version.Revision {
		return nil, fmt.Errorf("package preset revision is not active")
	}
	all, _ := p.Setups()
	for _, selection := range all {
		if selection.ID == id {
			return selection.Clone(), nil
		}
	}
	return nil, fmt.Errorf("unknown package preset")
}

type protocolResponse struct {
	Protocol int      `json:"protocol"`
	Tools    []string `json:"tools,omitempty"`
	Version  string   `json:"version,omitempty"`
	Result   string   `json:"result,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func decodeProtocol(result terminal.IsolatedResult) (protocolResponse, error) {
	var out protocolResponse
	if result.TimedOut {
		return out, fmt.Errorf("package tool exceeded its execution timeout")
	}
	if result.Truncated {
		return out, fmt.Errorf("package tool output exceeded %d bytes", terminal.MaxOutputBytes)
	}
	if result.ExitCode != 0 {
		return out, fmt.Errorf("package tool exited with code %d: %s", result.ExitCode, result.Stderr)
	}
	d := json.NewDecoder(strings.NewReader(result.Stdout))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, fmt.Errorf("package returned invalid protocol JSON")
	}
	if d.Decode(new(any)) != io.EOF || out.Protocol != 1 {
		return out, fmt.Errorf("package protocol version or framing mismatch")
	}
	if out.Error != "" {
		return out, fmt.Errorf("package refused the call: %s", out.Error)
	}
	return out, nil
}
func shellArgument(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func commandFor(spec ToolSpec) string {
	parts := []string{}
	switch spec.Interpreter {
	case "sh":
		parts = append(parts, "/bin/sh")
	case "python3":
		parts = append(parts, "/usr/bin/python3")
	case "node":
		parts = append(parts, "/usr/bin/node")
	}
	parts = append(parts, "/run/luna/package/"+spec.Entry)
	parts = append(parts, spec.Args...)
	for i := range parts {
		parts[i] = shellArgument(parts[i])
	}
	return "exec " + strings.Join(parts, " ")
}
func (p *Capability) Probe(ctx context.Context) error {
	checked := map[string]bool{}
	for _, spec := range p.version.Manifest.Tools {
		key := commandFor(spec)
		if checked[key] {
			continue
		}
		checked[key] = true
		input, _ := json.Marshal(map[string]any{"protocol": 1, "method": "describe"})
		result, err := terminal.ExecuteIsolated(ctx, terminal.IsolatedRequest{Command: key, Stdin: append(input, '\n'), Readonly: []terminal.ReadOnlyMount{{Source: p.version.Root, Target: "/run/luna/package"}}, Timeout: 3 * time.Second})
		if err != nil {
			return err
		}
		reply, err := decodeProtocol(result)
		if err != nil {
			return err
		}
		expected := []string{}
		for _, tool := range p.version.Manifest.Tools {
			if commandFor(tool) == key {
				expected = append(expected, tool.Name)
			}
		}
		sort.Strings(expected)
		sort.Strings(reply.Tools)
		if !equalNames(expected, reply.Tools) || reply.Version != "" && reply.Version != p.version.Manifest.Version {
			return fmt.Errorf("package handshake does not match its manifest")
		}
	}
	return nil
}
func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type packageTool struct {
	owner *Capability
	spec  ToolSpec
}

func (t packageTool) Name() string { return t.owner.version.Manifest.ToolName(t.spec.Name) }
func (t packageTool) Description() string {
	return t.spec.Description + " Package: " + t.owner.version.Manifest.Title + " (" + t.owner.version.Manifest.Version + "). This tool always runs isolated with its declared access; it cannot grant permissions."
}
func (t packageTool) Schema() *jsonschema.Schema {
	raw, _ := json.Marshal(t.spec.Input)
	var schema jsonschema.Schema
	_ = json.Unmarshal(raw, &schema)
	return &schema
}
func (t packageTool) Invoke(ctx context.Context, arguments string) (string, error) {
	if err := t.spec.Input.ValidateJSON(json.RawMessage(arguments)); err != nil {
		return "", err
	}
	access := t.spec.Access
	kinds := []plugin.AccessKind{plugin.AccessExec}
	if access.Read || access.State != "" {
		kinds = append(kinds, plugin.AccessRead)
	}
	if access.Write || access.State == "write" {
		kinds = append(kinds, plugin.AccessWrite)
	}
	if access.Network {
		kinds = append(kinds, plugin.AccessNetwork)
	}
	if err := plugin.CheckAccess(ctx, kinds...); err != nil {
		return "", err
	}
	if _, err := t.owner.store.Version(t.owner.version.Manifest.ID, t.owner.version.Revision); err != nil {
		return "", plugin.Unavailable(err)
	}
	run, _ := plugin.Run(ctx)
	roots := []string{}
	writable := []string{}
	dir := "/tmp/work"
	scopeApproval := access.State != ""
	if access.Read {
		roots = append(roots, plugin.Roots(ctx)...)
		if len(roots) == 0 {
			return "", fmt.Errorf("package tool requires a project workspace")
		}
		resolved, err := fileread.ResolveDirInRoots(roots, ".")
		if err != nil {
			return "", err
		}
		dir = resolved.Path
	}
	if access.Write {
		dirs := access.WriteDirs
		if len(dirs) == 0 {
			dirs = []string{"."}
		}
		for _, path := range dirs {
			resolved, err := fileread.ResolveDirInRoots(roots, path)
			if err != nil {
				return "", err
			}
			writable = append(writable, resolved.Path)
			automatic := false
			for _, allowed := range run.AutomaticWriteDirs {
				if fileread.Within(allowed, resolved.Path) {
					automatic = true
					break
				}
			}
			if !automatic {
				scopeApproval = true
			}
		}
		if run.WriteScopeError != "" {
			return "", fmt.Errorf("automatic write scope is unavailable")
		}
	}
	stateDir := ""
	if access.State != "" {
		roots = append(roots, t.owner.stateDir)
		stateDir = t.owner.stateDir
		if access.State == "write" {
			writable = append(writable, t.owner.stateDir)
		}
	}
	preview := t.owner.version.Manifest.ID + " @ " + t.owner.version.Revision + "\n" + arguments
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: t.Name(), Summary: "execute isolated package tool", Target: t.spec.Name, Preview: preview, ReadRoots: roots, WriteRoots: writable, Permissions: kinds, ScopeApproval: scopeApproval, ParametersDigest: plugin.AccessDigest(t.owner.version.Revision + "\n" + arguments)}); err != nil {
		return "", err
	}
	if _, err := t.owner.store.Version(t.owner.version.Manifest.ID, t.owner.version.Revision); err != nil {
		return "", plugin.Unavailable(err)
	}
	input, err := json.Marshal(struct {
		Protocol  int             `json:"protocol"`
		Method    string          `json:"method"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		StateDir  string          `json:"state_dir,omitempty"`
	}{1, "call", t.spec.Name, json.RawMessage(arguments), stateDir})
	if err != nil {
		return "", err
	}
	result, err := terminal.ExecuteIsolated(ctx, terminal.IsolatedRequest{Command: commandFor(t.spec), Dir: dir, ReadRoots: roots, WriteRoots: writable, Readonly: []terminal.ReadOnlyMount{{Source: t.owner.version.Root, Target: "/run/luna/package"}}, Stdin: append(input, '\n'), Network: access.Network, Timeout: time.Duration(t.spec.TimeoutSeconds) * time.Second})
	if err != nil {
		return "", err
	}
	reply, err := decodeProtocol(result)
	if err != nil {
		return "", err
	}
	return reply.Result, nil
}
