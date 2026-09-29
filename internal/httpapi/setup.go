package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/runconfig"
	"github.com/Qaraku/luna-agent/internal/store"
)

const originSetup = "setup"

func sessionSetupPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/api/sessions/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/setup")
	return id, ok && id != "" && !strings.Contains(id, "/")
}
func (s *Server) setupProvider(owner string) (plugin.SetupProvider, error) {
	if s.capabilities == nil {
		return nil, fmt.Errorf("setup source is unavailable")
	}
	entry, ok := s.capabilities.Entry(owner)
	if !ok || entry.State != plugin.StateEnabled {
		return nil, fmt.Errorf("setup source %q is disabled or unavailable; enable it or clear the session selection", owner)
	}
	provider, ok := entry.Plugin.(plugin.SetupProvider)
	if !ok {
		return nil, fmt.Errorf("capability %q does not provide run setups", owner)
	}
	return provider, nil
}
func (s *Server) setupForRun(cfg *store.ConfigRecord) (*runconfig.Selection, error) {
	if cfg == nil || cfg.Setup == nil {
		return nil, nil
	}
	selection := cfg.Setup.Clone()
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.setupProvider(selection.Owner); err != nil {
		return nil, err
	}
	return selection, nil
}

func (s *Server) setSessionSetup(w http.ResponseWriter, r *http.Request, id string) {
	var in *struct {
		Owner          string `json:"owner"`
		ID             string `json:"id"`
		Revision       string `json:"revision"`
		Reset          bool   `json:"reset"`
		ResetOverrides bool   `json:"reset_overrides"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in == nil || (in.Reset && (in.Owner != "" || in.ID != "" || in.Revision != "" || in.ResetOverrides)) || (!in.Reset && (in.Owner == "" || in.ID == "")) {
		fail(w, 400, fmt.Errorf("provide owner and id, or reset=true, not both"))
		return
	}
	var selected *runconfig.Selection
	if !in.Reset {
		provider, err := s.setupProvider(in.Owner)
		if err != nil {
			fail(w, 409, err)
			return
		}
		selected, err = provider.ResolveSetup(in.ID, in.Revision)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if selected == nil || selected.Owner != in.Owner || selected.ID != in.ID {
			fail(w, 500, fmt.Errorf("setup source returned an inconsistent identity"))
			return
		}
		selected = selected.Clone()
		if err = selected.Validate(); err != nil {
			fail(w, 400, err)
			return
		}
		if selected.Model != "" && !s.configuredModel(selected.Model) {
			fail(w, 400, fmt.Errorf("setup model %q is not configured", selected.Model))
			return
		}
	}
	s.runMu.Lock()
	if s.busy && s.sessionID == id {
		s.runMu.Unlock()
		fail(w, 409, fmt.Errorf("stop the current session run before changing its setup"))
		return
	}
	// 选择不修改实例启用状态、权限矩阵或自动写入范围。
	status, err := s.updateSessionConfig(id, func(next *store.ConfigRecord) (int, error) {
		next.Setup = selected
		if in.ResetOverrides {
			next.Model = ""
			next.ReasoningEffort = nil
		}
		return 200, nil
	})
	s.runMu.Unlock()
	if err != nil {
		fail(w, status, err)
		return
	}
	send(w, 200, map[string]any{"session_id": id, "selection": selected})
}

type setupView struct {
	Selection    *runconfig.Selection         `json:"selection"`
	Active       bool                         `json:"active"`
	Capabilities []string                     `json:"available_capabilities"`
	Unavailable  []string                     `json:"unavailable_capabilities"`
	Tools        []string                     `json:"available_tools"`
	Resources    map[string][]string          `json:"available_resources"`
	Pending      bool                         `json:"snapshot_pending"`
	Revisions    map[string]map[string]string `json:"resource_revisions,omitempty"`
	Problem      string                       `json:"problem,omitempty"`
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("session"))
	view := setupView{Capabilities: []string{}, Unavailable: []string{}, Tools: []string{}, Resources: map[string][]string{}}
	s.runMu.Lock()
	execution, err := s.executionViewLocked(id)
	view.Active = s.busy && s.sessionID == id
	frozen := s.activeConfiguration.Clone()
	s.runMu.Unlock()
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	if execution.config != nil {
		view.Selection = execution.config.Setup.Clone()
	}
	if view.Active {
		if frozen == nil {
			view.Pending = true
		} else {
			view.Selection = frozen.Selection
			view.Capabilities = frozen.Capabilities
			view.Tools = frozen.Tools
			view.Resources = frozen.Resources
			view.Revisions = frozen.ResourceRevisions
		}
		send(w, 200, view)
		return
	}
	if view.Selection != nil {
		if err := view.Selection.Validate(); err != nil {
			fail(w, 400, err)
			return
		}
		if _, err := s.setupProvider(view.Selection.Owner); err != nil {
			view.Problem = err.Error()
		}
	}

	enabled := map[string]bool{}
	for _, source := range pluginhost.Allowlist {
		if view.Selection.AllowsTool(source.Tool) {
			view.Tools = append(view.Tools, source.Tool)
		}
	}
	if s.capabilities != nil {
		for _, entry := range s.capabilities.Enabled() {
			if !view.Selection.AllowsCapability(entry.Descriptor.ID) {
				continue
			}
			enabled[entry.Descriptor.ID] = true
			view.Capabilities = append(view.Capabilities, entry.Descriptor.ID)
			if provider, ok := entry.Plugin.(plugin.ToolProvider); ok {
				for _, tool := range provider.Tools() {
					if view.Selection.AllowsTool(tool.Name()) {
						view.Tools = append(view.Tools, tool.Name())
					}
				}
			}
			if provider, ok := entry.Plugin.(plugin.RunResourceProvider); ok {
				names, err := provider.RunResources(context.Background())
				if err != nil {
					fail(w, 500, err)
					return
				}
				selected := []string{}
				for _, name := range names {
					if view.Selection.AllowsResource(entry.Descriptor.ID, name) {
						selected = append(selected, name)
					}
				}
				view.Resources[entry.Descriptor.ID] = selected
			}
		}
	}
	if view.Selection != nil {
		for _, id := range view.Selection.Capabilities {
			if !enabled[id] {
				view.Unavailable = append(view.Unavailable, id)
			}
		}
	}
	send(w, 200, view)
}

func selectedModel(cfg *store.ConfigRecord) (string, string) {
	if cfg != nil {
		if cfg.Model != "" {
			return cfg.Model, originSession
		}
		if cfg.Setup != nil && cfg.Setup.Model != "" {
			return cfg.Setup.Model, originSetup
		}
	}
	return "", originGlobal
}
func selectedReasoning(cfg *store.ConfigRecord) (*string, string) {
	if cfg != nil {
		if cfg.ReasoningEffort != nil {
			return cfg.ReasoningEffort, originSession
		}
		if cfg.Setup != nil && cfg.Setup.ReasoningEffort != nil {
			return cfg.Setup.ReasoningEffort, originSetup
		}
	}
	return nil, originGlobal
}

func (s *Server) listSetups(w http.ResponseWriter, r *http.Request) {
	type choice struct {
		runconfig.Selection
		SourceTitle string `json:"source_title"`
	}
	choices := []choice{}
	if s.capabilities != nil {
		for _, entry := range s.capabilities.Enabled() {
			source, ok := entry.Plugin.(plugin.SetupCatalog)
			if !ok {
				continue
			}
			items, err := source.Setups()
			if err != nil {
				fail(w, 500, err)
				return
			}
			if len(choices)+len(items) > 256 {
				fail(w, 500, fmt.Errorf("setup catalog exceeds 256 items"))
				return
			}
			for _, selection := range items {
				if selection.Owner != entry.Descriptor.ID || selection.Validate() != nil {
					fail(w, 500, fmt.Errorf("setup source returned invalid metadata"))
					return
				}
				choices = append(choices, choice{*selection.Clone(), entry.Descriptor.Title})
			}
		}
	}
	send(w, 200, map[string]any{"presets": choices})
}
