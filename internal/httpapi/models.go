package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/store"
)

// ModelRef is one model a run may be sent to, in the shape the browser reads.
// The first entry of the configuration is the default, and Default marks it
// rather than leaving the interface to infer it from position: the list is the
// only thing the browser sees.
type ModelRef struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Default  bool   `json:"default"`
}

// currentModel says which model the next run would use, and where that choice
// came from. A user who switched a session and came back to it later needs to
// know whether they are looking at their own choice or the configuration's.
type currentModel struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
}

type modelsResponse struct {
	Models  []ModelRef   `json:"models"`
	Current currentModel `json:"current"`
}

// The two origins a model choice can have.
const (
	originSession = "session"
	originGlobal  = "global"
)

// providerConfigNow returns the configuration a run started right now would use.
//
// The second value reports whether this server has a source at all: a server
// built without one — a test, or an embedder that owns the provider itself —
// reports the configuration it was built with, and callers fall back to Info in
// that case. An error is only ever returned together with live=true.
func (s *Server) providerConfigNow() (config.Config, bool, error) {
	if s.configSource == nil {
		return config.Config{}, false, nil
	}
	cfg, err := s.configSource.Current()
	if err != nil {
		return config.Config{}, true, err
	}
	return cfg, true, nil
}

// modelsNow is the list a run started now could be sent to, the default first.
//
// It is read on every request rather than cached at startup, because the provider
// file can change while this process runs: a list cached once would offer a model
// the next run cannot be sent to, or miss one it could.
func (s *Server) modelsNow() ([]ModelRef, bool, error) {
	cfg, live, err := s.providerConfigNow()
	if err != nil {
		return nil, true, err
	}
	if !live {
		refs := make([]ModelRef, 0, len(s.info.Models))
		for i, model := range s.info.Models {
			refs = append(refs, ModelRef{Name: model.Name, Provider: model.Provider, Default: i == 0})
		}
		return refs, false, nil
	}
	refs := make([]ModelRef, 0, len(cfg.Models))
	for i, model := range cfg.Models {
		refs = append(refs, ModelRef{Name: model.Name, Provider: model.Provider, Default: i == 0})
	}
	return refs, true, nil
}

// defaultModelNow is the model a run uses when nothing else is chosen: the first
// entry of the live list, or the model this server was built with when there is no
// source. It is empty for a Luna with no provider in use, which a run reports as
// an unconfigured provider rather than as a model choice.
func (s *Server) defaultModelNow() (string, error) {
	refs, live, err := s.modelsNow()
	if err != nil {
		return "", err
	}
	if len(refs) > 0 {
		return refs[0].Name, nil
	}
	if live {
		return "", nil
	}
	return s.info.Model, nil
}

// configuredModel reports whether name is one of the models this runtime may be
// asked for right now.
func (s *Server) configuredModel(name string) bool {
	refs, live, err := s.modelsNow()
	if err != nil {
		return false
	}
	for _, model := range refs {
		if model.Name == name {
			return true
		}
	}
	return !live && len(refs) == 0 && name == s.info.Model
}

// sendModels reports the configured models and which one a session would use.
//
// The session is optional: without it the answer describes the configuration
// alone, which is what a browser that has not opened a session yet can ask.
func (s *Server) sendModels(w http.ResponseWriter, r *http.Request) {
	views, _, err := s.modelsNow()
	if err != nil {
		fail(w, 500, err)
		return
	}
	name, err := s.defaultModelNow()
	if err != nil {
		fail(w, 500, err)
		return
	}
	current := currentModel{Name: name, Origin: originGlobal}
	if id := strings.TrimSpace(r.URL.Query().Get("session")); id != "" {
		session, err := s.sessions.Read(id)
		if err != nil {
			fail(w, sessionStatus(err), err)
			return
		}
		// A session that chose a model keeps that choice even if the
		// configuration has changed since: saying otherwise would report a
		// model the next run is not going to use.
		if name, origin := selectedModel(session.Config); name != "" {
			current = currentModel{Name: name, Origin: origin}
		}
	}
	send(w, 200, modelsResponse{Models: views, Current: current})
}

// setSessionModel records which model this session's next runs should use. It is
// a mutation, so it demands the exact origin, and it appends rather than
// rewrites: the session file is append-only, and "this session moved to that
// model" is something that happened, in order.
func (s *Server) setSessionModel(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		Model string `json:"model"`
		Reset bool   `json:"reset"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Model)
	if (name == "" && !in.Reset) || (name != "" && in.Reset) {
		fail(w, 400, fmt.Errorf("provide model or reset=true, not both"))
		return
	}
	if _, _, err := s.modelsNow(); err != nil {
		// The list could not be read at all, so "unknown model" would be a
		// guess about what this runtime offers.
		fail(w, 500, err)
		return
	}
	if !in.Reset && !s.configuredModel(name) {
		fail(w, 400, fmt.Errorf("unknown model %q: this runtime was configured with %s", name, s.configuredModelNames()))
		return
	}
	selected, origin := name, originSession
	if in.Reset {
		var err error
		selected, err = s.defaultModelNow()
		if err != nil {
			fail(w, 500, err)
			return
		}
		origin = originGlobal
	}
	status, err := s.updateSessionConfig(id, func(next *store.ConfigRecord) (int, error) {
		next.Model = name
		if in.Reset {
			if inherited, inheritedOrigin := selectedModel(next); inherited != "" {
				selected, origin = inherited, inheritedOrigin
			}
		}
		return http.StatusOK, nil
	})
	if err != nil {
		fail(w, status, err)
		return
	}
	send(w, 200, map[string]string{"session_id": id, "model": selected, "origin": origin})
}

// configuredModelNames names what could have been chosen instead, so a refused
// switch tells the user what would work.
func (s *Server) configuredModelNames() string {
	refs, live, err := s.modelsNow()
	if err != nil {
		// Unreachable from the refusal path, which reads the list first: a
		// message that cannot name the alternatives still must not claim there
		// are none.
		return "no model this runtime could read"
	}
	if len(refs) == 0 {
		if live {
			return "no model: no provider is configured yet"
		}
		return fmt.Sprintf("%q", s.info.Model)
	}
	names := make([]string, 0, len(refs))
	for _, model := range refs {
		names = append(names, model.Name)
	}
	return strings.Join(names, ", ")
}

// sessionModelPath extracts the id from /api/sessions/{id}/model.
func sessionModelPath(path string) (string, bool) {
	const prefix = "/api/sessions/"
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/model")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
