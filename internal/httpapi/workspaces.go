package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/workspace"
)

// workspacesPath is the interface's own path for listing and defining
// workspaces. It is not a capability route: a workspace is data the user owns,
// and the composition root is what knows where it is stored.
const workspacesPath = "/api/workspaces"

// WorkspaceStore is the workspaces as this layer uses them: what exists, and
// how to define one more. The composition root supplies it, because storing a
// workspace and handing it to the capability are the same act.
type WorkspaceStore interface {
	// List returns every workspace, in the order it was defined.
	List() []workspace.Workspace
	// Get returns the workspace with this id; found is false when there is
	// none, which is an answer rather than a failure.
	Get(id string) (workspace.Workspace, bool)
	// Create defines a workspace. workspace.ErrNameTaken reports a name
	// another workspace already uses.
	Create(name string, dirs []string) (workspace.Workspace, error)
}

// WithWorkspaces supplies the workspace store. A server built without one lists
// no workspaces and refuses to define or assign any, rather than failing to
// start.
func WithWorkspaces(workspaces WorkspaceStore) Option {
	return func(s *Server) { s.workspaces = workspaces }
}

// workspaceDirs returns the directories a workspace makes readable.
//
// A server with no workspace store answers false, the same answer as a workspace
// that is not there. That is deliberate: the caller fails the run rather than
// falling back to the configured root, so a session can never end up reading
// somewhere its binding did not name.
func (s *Server) workspaceDirs(id string) ([]string, bool) {
	if s.workspaces == nil || id == "" {
		return nil, false
	}
	found, ok := s.workspaces.Get(id)
	if !ok {
		return nil, false
	}
	return found.Dirs, true
}

// workspaceView is one workspace as the browser reads it: the id it points with,
// the name the user reads, and the directories. The directories are absolute
// paths — the user gave them and the interface shows them back — while the text
// a *model* sees carries only directory names, which is what keeps one machine's
// layout out of every prompt.
type workspaceView struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Dirs []string `json:"dirs"`
}

type workspacesResponse struct {
	Workspaces []workspaceView `json:"workspaces"`
}

// workspaceViewOf renders one workspace for the browser. An empty directory list
// is written as an empty list rather than omitted, so a client never has to tell
// "no directories" apart from "nothing was sent".
func workspaceViewOf(item workspace.Workspace) workspaceView {
	dirs := item.Dirs
	if dirs == nil {
		dirs = []string{}
	}
	return workspaceView{ID: item.ID, Name: item.Name, Dirs: dirs}
}

// sendWorkspaces lists every workspace. A server with no store answers with an
// empty list: not knowing about workspaces and having none are the same thing to
// a client that only reads them.
func (s *Server) sendWorkspaces(w http.ResponseWriter) {
	views := make([]workspaceView, 0)
	if s.workspaces != nil {
		for _, item := range s.workspaces.List() {
			views = append(views, workspaceViewOf(item))
		}
	}
	send(w, 200, workspacesResponse{Workspaces: views})
}

// createWorkspace defines a new workspace and answers with it.
//
// A workspace that cannot be created is a 400 with the reason: what was wrong
// with the directories or the name is something the user has to be able to fix
// from the message alone. A name another workspace already holds is a 409 — the
// request was well-formed and conflicted with what exists — and the message
// names the holder, so the user can find the workspace they meant instead of
// inventing a second label for the same thing.
func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.workspaces == nil {
		fail(w, 500, fmt.Errorf("no workspace store is configured"))
		return
	}
	var in struct {
		Name string   `json:"name"`
		Dirs []string `json:"dirs"`
	}
	if !decode(w, r, &in) {
		return
	}
	created, err := s.workspaces.Create(in.Name, in.Dirs)
	if err != nil {
		if errors.Is(err, workspace.ErrNameTaken) {
			fail(w, 409, err)
			return
		}
		fail(w, 400, err)
		return
	}
	s.addEvent("workspace_created", fmt.Sprintf("workspace %q was defined over %d directories", created.Name, len(created.Dirs)))
	send(w, 200, workspaceViewOf(created))
}

// sessionWorkspacePath extracts the id from /api/sessions/{id}/workspace. The
// remainder is handed on unread: whether a session by that id exists is the
// store's answer, and parsing a path is not a way to know it.
func sessionWorkspacePath(path string) (string, bool) {
	const prefix = "/api/sessions/"
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/workspace")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// setSessionWorkspace binds a session to a workspace, or clears the binding when
// the request names none.
//
// The new record is *merged* into the session's current configuration rather
// than appended by itself. Both fields live in one record and the newest record
// is the one in force, so a record that named only a workspace would be read as
// "this session also has no model" — and the next run would silently fall back
// to the configured default, losing a choice the user made. Reading the current
// record first and carrying its model over is what keeps "which model" and
// "which workspace" independent choices about the same run. The mirror-image
// case is handled where the model is chosen, for the same reason.
//
// An empty workspace clears the binding: a session that is not working in any
// particular workspace is the state every session starts in, and it has to stay
// reachable. The id otherwise has to name a workspace that exists; a session
// pointing at nothing would look like a workspace that failed to load.
func (s *Server) setSessionWorkspace(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		Workspace string `json:"workspace"`
	}
	if !decode(w, r, &in) {
		return
	}
	session, err := s.sessions.Read(id)
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	target := strings.TrimSpace(in.Workspace)
	var view *workspaceView
	if target != "" {
		if s.workspaces == nil {
			fail(w, 500, fmt.Errorf("no workspace store is configured"))
			return
		}
		found, ok := s.workspaces.Get(target)
		if !ok {
			fail(w, 404, fmt.Errorf("unknown workspace %q", target))
			return
		}
		rendered := workspaceViewOf(found)
		view = &rendered
	}
	record := store.ConfigRecord{Type: store.TypeConfig, Workspace: target, At: time.Now()}
	if current := session.Config; current != nil {
		// The model choice is carried over, never re-derived here: this request
		// is about the workspace, and the model it happens to use is whatever
		// the session's newest config record already said.
		record.Model = current.Model
	}
	if err := s.sessions.AppendConfig(id, record); err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	if view != nil {
		s.addEvent("session_workspace", fmt.Sprintf("session %s now works in workspace %q", id, view.Name))
	} else {
		s.addEvent("session_workspace", fmt.Sprintf("session %s is no longer bound to a workspace", id))
	}
	send(w, 200, map[string]any{"session_id": id, "workspace": view})
}

// sessionWorkspace renders the workspace a session's replay response reports.
//
// The association is stored as an id in the session's config record, and the
// playback object reports the whole workspace, so a client does not have to read
// a config record and join it against the workspace list to know where a session
// works. It is null when the session is bound to nothing, and also when the id
// no longer names a workspace — a deleted workspace is not a binding.
func (s *Server) sessionWorkspace(session store.Session) *workspaceView {
	if s.workspaces == nil || session.Config == nil || session.Config.Workspace == "" {
		return nil
	}
	found, ok := s.workspaces.Get(session.Config.Workspace)
	if !ok {
		return nil
	}
	view := workspaceViewOf(found)
	return &view
}
