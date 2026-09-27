package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

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

// defaultModel is the model a run uses when nothing else is chosen: the first
// configured entry, or the single model an environment-only configuration has.
func (s *Server) defaultModel() string {
	if len(s.info.Models) > 0 {
		return s.info.Models[0].Name
	}
	return s.info.Model
}

// configuredModel reports whether name is one of the models this runtime may be
// asked for.
func (s *Server) configuredModel(name string) bool {
	for _, model := range s.info.Models {
		if model.Name == name {
			return true
		}
	}
	return len(s.info.Models) == 0 && name == s.info.Model
}

// sendModels reports the configured models and which one a session would use.
//
// The session is optional: without it the answer describes the configuration
// alone, which is what a browser that has not opened a session yet can ask.
func (s *Server) sendModels(w http.ResponseWriter, r *http.Request) {
	views := make([]ModelRef, 0, len(s.info.Models))
	for i, model := range s.info.Models {
		views = append(views, ModelRef{Name: model.Name, Provider: model.Provider, Default: i == 0})
	}
	current := currentModel{Name: s.defaultModel(), Origin: originGlobal}
	if id := strings.TrimSpace(r.URL.Query().Get("session")); id != "" {
		session, err := s.sessions.Read(id)
		if err != nil {
			fail(w, sessionStatus(err), err)
			return
		}
		// A session that chose a model keeps that choice even if the
		// configuration has changed since: saying otherwise would report a
		// model the next run is not going to use.
		if session.Config != nil && session.Config.Model != "" {
			current = currentModel{Name: session.Config.Model, Origin: originSession}
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
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Model)
	if name == "" {
		fail(w, 400, fmt.Errorf("model is required"))
		return
	}
	if !s.configuredModel(name) {
		fail(w, 400, fmt.Errorf("unknown model %q: this runtime was configured with %s", name, s.configuredModelNames()))
		return
	}
	exists, err := s.sessions.Exists(id)
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	if !exists {
		fail(w, 404, fmt.Errorf("unknown session %q", id))
		return
	}
	record := store.ConfigRecord{Type: store.TypeConfig, Model: name, At: time.Now()}
	// The workspace binding is carried over rather than dropped: both fields
	// live in one record and the newest record is the one in force, so a record
	// that named only a model would detach the session from its workspace and
	// its next run would fall back to the default project. Choosing a model and
	// choosing a workspace are independent decisions about the same run; each
	// writer changes its own field and carries the other one over.
	if session, err := s.sessions.Read(id); err == nil && session.Config != nil {
		record.Workspace = session.Config.Workspace
	}
	if err := s.sessions.AppendConfig(id, record); err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	send(w, 200, map[string]string{"session_id": id, "model": name, "origin": originSession})
}

// configuredModelNames names what could have been chosen instead, so a refused
// switch tells the user what would work.
func (s *Server) configuredModelNames() string {
	if len(s.info.Models) == 0 {
		return fmt.Sprintf("%q", s.info.Model)
	}
	names := make([]string, 0, len(s.info.Models))
	for _, model := range s.info.Models {
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
