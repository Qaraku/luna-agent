// Package runconfig 定义运行时的资源选择，不负责安装、授权或任何能力的业务语义。
package runconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Qaraku/luna-agent/internal/config"
)

const (
	MaxInstructionsBytes = 8 * 1024
	MaxSelectionBytes    = 32 * 1024
	MaxResources         = 128
)

var identifier = regexp.MustCompile(`^[a-z][a-zA-Z0-9_.:-]{0,127}$`)

// Selection 是用户选择的组合快照。nil 列表表示继承当前可用集合，空列表表示不选。
// 这里只能缩小已启用资源集合，字段中不含权限或凭据。
type Selection struct {
	Owner           string              `json:"owner,omitempty"`
	ID              string              `json:"id"`
	Revision        string              `json:"revision,omitempty"`
	Title           string              `json:"title,omitempty"`
	Instructions    string              `json:"instructions,omitempty"`
	Model           string              `json:"model,omitempty"`
	ReasoningEffort *string             `json:"reasoning_effort,omitempty"`
	Capabilities    []string            `json:"capabilities"`
	Tools           []string            `json:"tools"`
	Resources       map[string][]string `json:"resources,omitempty"`
}

// Snapshot 记录本轮实际可用集合，不把后续设置或后续目录内容冒充本轮状态。
type Snapshot struct {
	WorkspaceID       string                       `json:"workspace_id,omitempty"`
	Selection         *Selection                   `json:"selection,omitempty"`
	Model             string                       `json:"model,omitempty"`
	ReasoningEffort   string                       `json:"reasoning_effort,omitempty"`
	Capabilities      []string                     `json:"capabilities"`
	Tools             []string                     `json:"tools"`
	Resources         map[string][]string          `json:"resources,omitempty"`
	ResourceRevisions map[string]map[string]string `json:"resource_revisions,omitempty"`
}

func (s *Selection) AllowsCapability(id string) bool {
	return s == nil || s.Capabilities == nil || slices.Contains(s.Capabilities, id)
}
func (s *Selection) AllowsTool(name string) bool {
	return s == nil || s.Tools == nil || slices.Contains(s.Tools, name)
}
func (s *Selection) AllowsResource(owner, name string) bool {
	if s == nil {
		return true
	}
	names, ok := s.Resources[owner]
	return !ok || names == nil || slices.Contains(names, name)
}

func (s *Selection) Clone() *Selection {
	if s == nil {
		return nil
	}
	next := *s
	next.Capabilities = slices.Clone(s.Capabilities)
	next.Tools = slices.Clone(s.Tools)
	if s.ReasoningEffort != nil {
		v := *s.ReasoningEffort
		next.ReasoningEffort = &v
	}
	if s.Resources != nil {
		next.Resources = make(map[string][]string, len(s.Resources))
		for k, v := range s.Resources {
			next.Resources[k] = slices.Clone(v)
		}
	}
	return &next
}

func (s *Selection) Validate() error {
	if s == nil {
		return nil
	}
	if !identifier.MatchString(s.ID) || (s.Owner != "" && !identifier.MatchString(s.Owner)) {
		return fmt.Errorf("invalid selection identity")
	}
	if len(s.Revision) > 128 || strings.ContainsAny(s.Revision, "/\\\x00\n\r") {
		return fmt.Errorf("invalid selection revision")
	}
	if !utf8.ValidString(s.Title) || utf8.RuneCountInString(s.Title) > 120 || strings.ContainsRune(s.Title, 0) {
		return fmt.Errorf("selection title must be valid text of at most 120 characters")
	}
	if !utf8.ValidString(s.Instructions) || strings.ContainsRune(s.Instructions, 0) || len(s.Instructions) > MaxInstructionsBytes {
		return fmt.Errorf("selection instructions must be text of at most %d bytes", MaxInstructionsBytes)
	}
	if len(s.Model) > 256 || strings.ContainsAny(s.Model, "\x00\n\r") {
		return fmt.Errorf("invalid selection model")
	}
	if s.ReasoningEffort != nil {
		if _, err := config.ParseReasoningEffort(*s.ReasoningEffort); err != nil {
			return err
		}
	}
	if err := validateNames(s.Capabilities); err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	if err := validateNames(s.Tools); err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	if len(s.Resources) > MaxResources {
		return fmt.Errorf("too many resource namespaces")
	}
	for owner, names := range s.Resources {
		if !identifier.MatchString(owner) {
			return fmt.Errorf("invalid resource namespace")
		}
		if err := validateNames(names); err != nil {
			return fmt.Errorf("resource %s: %w", owner, err)
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(data) > MaxSelectionBytes {
		return fmt.Errorf("selection exceeds %d bytes", MaxSelectionBytes)
	}
	return nil
}
func validateNames(names []string) error {
	if len(names) > MaxResources {
		return fmt.Errorf("more than %d resources", MaxResources)
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !identifier.MatchString(name) {
			return fmt.Errorf("invalid resource name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate resource %q", name)
		}
		seen[name] = true
	}
	return nil
}

func (s *Snapshot) Clone() *Snapshot {
	if s == nil {
		return nil
	}
	next := *s
	next.Selection = s.Selection.Clone()
	next.Capabilities = slices.Clone(s.Capabilities)
	next.Tools = slices.Clone(s.Tools)
	if s.Resources != nil {
		next.Resources = make(map[string][]string, len(s.Resources))
		for k, v := range s.Resources {
			next.Resources[k] = slices.Clone(v)
		}
	}
	if s.ResourceRevisions != nil {
		next.ResourceRevisions = map[string]map[string]string{}
		for owner, versions := range s.ResourceRevisions {
			next.ResourceRevisions[owner] = map[string]string{}
			for name, revision := range versions {
				next.ResourceRevisions[owner][name] = revision
			}
		}
	}
	return &next
}
