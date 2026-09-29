// Package packages 管理用户安装的能力包及不可变版本；安装不执行包内代码。
package packages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Qaraku/luna-agent/internal/runconfig"
)

const ManifestName = "luna-package.json"
const MaxManifestBytes = 64 * 1024
const MaxPackageFiles = 128
const MaxPackageFileBytes = 32 * 1024 * 1024
const MaxPackageBytes = 64 * 1024 * 1024

var packageName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
var localName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
var parameterName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

type ToolAccess struct {
	Read      bool     `json:"read,omitempty"`
	Write     bool     `json:"write,omitempty"`
	Network   bool     `json:"network,omitempty"`
	State     string   `json:"state,omitempty"`
	WriteDirs []string `json:"write_dirs,omitempty"`
}
type ToolSpec struct {
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Entry          string     `json:"entry"`
	Interpreter    string     `json:"interpreter,omitempty"`
	Args           []string   `json:"args,omitempty"`
	Input          Schema     `json:"input_schema"`
	Access         ToolAccess `json:"access"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty"`
}
type Surface struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Entry  string `json:"entry"`
	Source string `json:"source,omitempty"`
}
type Manifest struct {
	Format      int                   `json:"format"`
	ID          string                `json:"id"`
	Version     string                `json:"version"`
	Title       string                `json:"title"`
	Description string                `json:"description,omitempty"`
	StateSchema int                   `json:"state_schema,omitempty"`
	Files       []string              `json:"files"`
	Tools       []ToolSpec            `json:"tools,omitempty"`
	Skills      []string              `json:"skills,omitempty"`
	Presets     []runconfig.Selection `json:"presets,omitempty"`
	Static      []string              `json:"static,omitempty"`
	Panels      []Surface             `json:"panels,omitempty"`
	Widgets     []Surface             `json:"widgets,omitempty"`
}

func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > MaxManifestBytes || !utf8.Valid(data) {
		return m, fmt.Errorf("manifest exceeds text limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid package manifest: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return m, fmt.Errorf("expected one package manifest")
	}
	return m, m.Validate()
}
func safePackagePath(name string) bool {
	if strings.IndexFunc(name, func(r rune) bool { return r < 32 || r == 127 }) >= 0 || strings.ContainsAny(name, "%?#") || name == receiptName {
		return false
	}
	if name == "" || !utf8.ValidString(name) || len(name) > 240 || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\n\r:") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || part == ".git" || part == "sessions" || part == "memories" {
			return false
		}
		base := strings.ToLower(part)
		if base == ".env" || strings.HasPrefix(base, ".env.") || base == "provider.yaml" || base == "auth.json" || base == "memory.jsonl" || base == "settings.yaml" || base == "credentials.json" {
			return false
		}
	}
	return true
}
func (m *Manifest) Validate() error {
	if strings.IndexFunc(m.Title+m.Version, func(r rune) bool { return r < 32 || r == 127 }) >= 0 || !utf8.ValidString(m.Title+m.Version) {
		return fmt.Errorf("package title and version must be plain text")
	}
	if m.Format != 1 || !packageName.MatchString(m.ID) || strings.TrimSpace(m.Title) == "" || len(m.Title) > 240 || strings.TrimSpace(m.Version) == "" || len(m.Version) > 64 || len(m.Description) > 4096 {
		return fmt.Errorf("invalid package format or identity")
	}
	if m.StateSchema == 0 {
		m.StateSchema = 1
	}
	if m.StateSchema < 1 {
		return fmt.Errorf("state_schema must be positive")
	}
	if len(m.Files) > MaxPackageFiles || len(m.Tools) > 16 || len(m.Skills) > 32 || len(m.Presets) > 16 || len(m.Static) > 64 || len(m.Panels) > 8 || len(m.Widgets) > 8 {
		return fmt.Errorf("package contribution limit exceeded")
	}
	files := map[string]bool{}
	for _, name := range m.Files {
		if !safePackagePath(name) || name == ManifestName || files[name] {
			return fmt.Errorf("invalid, private or duplicate package file %q", name)
		}
		files[name] = true
	}
	tools := map[string]bool{}
	for i := range m.Tools {
		t := &m.Tools[i]
		if !localName.MatchString(t.Name) || tools[t.Name] || !files[t.Entry] || strings.TrimSpace(t.Description) == "" || len(t.Description) > 4096 {
			return fmt.Errorf("invalid tool declaration")
		}
		tools[t.Name] = true
		switch t.Interpreter {
		case "", "sh", "python3", "node":
		default:
			return fmt.Errorf("unsupported tool interpreter")
		}
		if len(t.Args) > 16 {
			return fmt.Errorf("too many fixed tool arguments")
		}
		for _, arg := range t.Args {
			if len(arg) > 1024 || strings.ContainsRune(arg, 0) {
				return fmt.Errorf("invalid fixed tool argument")
			}
		}
		if t.Input.Type != "object" {
			return fmt.Errorf("tool input root must be an object")
		}
		if err := t.Input.normalize(0); err != nil {
			return err
		}
		if t.Access.Write && !t.Access.Read {
			return fmt.Errorf("project write also requires read")
		}
		if len(t.Access.WriteDirs) > 16 || len(t.Access.WriteDirs) > 0 && !t.Access.Write {
			return fmt.Errorf("invalid writable directory declaration")
		}
		for _, dir := range t.Access.WriteDirs {
			if dir != "." && !safePackagePath(dir) {
				return fmt.Errorf("write_dirs must remain project-relative")
			}
		}
		switch t.Access.State {
		case "", "read", "write":
		default:
			return fmt.Errorf("state access must be read or write")
		}
		if t.TimeoutSeconds == 0 {
			t.TimeoutSeconds = 30
		}
		if t.TimeoutSeconds < 1 || t.TimeoutSeconds > 120 {
			return fmt.Errorf("tool timeout must be 1..120 seconds")
		}
	}
	skills := map[string]bool{}
	for _, dir := range m.Skills {
		if !strings.HasPrefix(dir, "skills/") || !localName.MatchString(strings.TrimPrefix(dir, "skills/")) || !files[dir+"/SKILL.md"] || skills[dir] {
			return fmt.Errorf("skills must name distinct declared skills/<name>/SKILL.md directories")
		}
		skills[dir] = true
	}
	presets := map[string]bool{}
	for i := range m.Presets {
		p := &m.Presets[i]
		if !localName.MatchString(p.ID) || presets[p.ID] {
			return fmt.Errorf("invalid preset id")
		}
		presets[p.ID] = true
		if err := p.Validate(); err != nil {
			return err
		}
	}
	static := map[string]bool{}
	for _, name := range m.Static {
		if !files[name] || static[name] {
			return fmt.Errorf("static resources must name declared package files")
		}
		static[name] = true
	}
	for _, surfaces := range [][]Surface{m.Panels, m.Widgets} {
		ids := map[string]bool{}
		for _, s := range surfaces {
			if !localName.MatchString(s.ID) || ids[s.ID] || s.Title == "" || len(s.Title) > 240 || !static[s.Entry] || (!strings.HasSuffix(s.Entry, ".js") && !strings.HasSuffix(s.Entry, ".mjs")) {
				return fmt.Errorf("invalid UI surface")
			}
			ids[s.ID] = true
			if s.Source != "" && !static[s.Source] {
				return fmt.Errorf("widget source must be a declared static resource")
			}
		}
	}
	return nil
}
func (m Manifest) CapabilityID() string         { return "pkg-" + m.ID }
func (m Manifest) ToolName(name string) string  { return "pkg_" + m.ID + "_" + name }
func (m Manifest) SkillName(name string) string { return "pkg_" + m.ID + "__" + name }
func (m Manifest) NeedsUITrust() bool {
	return len(m.Static) > 0 || len(m.Panels) > 0 || len(m.Widgets) > 0
}
func (m Manifest) AccessSummary() []string {
	seen := map[string]bool{}
	if len(m.Tools) > 0 {
		seen["exec"] = true
	}
	for _, t := range m.Tools {
		if t.Access.Read {
			seen["read"] = true
		}
		if t.Access.Write {
			seen["write"] = true
		}
		if t.Access.Network {
			seen["network"] = true
		}
		if t.Access.State != "" {
			seen["state."+t.Access.State] = true
		}
	}
	if m.NeedsUITrust() {
		seen["trusted-ui"] = true
	}
	out := []string{}
	for _, name := range []string{"exec", "read", "write", "network", "state.read", "state.write", "trusted-ui"} {
		if seen[name] {
			out = append(out, name)
		}
	}
	return out
}
