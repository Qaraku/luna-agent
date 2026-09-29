package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Qaraku/luna-agent/internal/store"
)

func sessionMetadataPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/api/sessions/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/metadata")
	return id, ok && id != "" && !strings.Contains(id, "/")
}
func (s *Server) setSessionMetadata(w http.ResponseWriter, r *http.Request, id string) {
	var in *struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in == nil || (in.Title == nil && in.Archived == nil) {
		fail(w, 400, fmt.Errorf("title or archived is required"))
		return
	}
	if in.Title != nil {
		title := strings.Join(strings.Fields(*in.Title), " ")
		if !utf8.ValidString(title) || title == "" || utf8.RuneCountInString(title) > store.TitleLimit || strings.ContainsRune(title, 0) {
			fail(w, 400, fmt.Errorf("title must contain 1..%d characters", store.TitleLimit))
			return
		}
		in.Title = &title
	}
	s.runMu.Lock()
	if in.Archived != nil && *in.Archived && s.busy && s.sessionID == id {
		s.runMu.Unlock()
		fail(w, 409, fmt.Errorf("stop the active session before archiving it"))
		return
	}
	status, err := s.updateSessionConfig(id, func(next *store.ConfigRecord) (int, error) {
		if in.Title != nil {
			next.TitleOverride = in.Title
		}
		if in.Archived != nil {
			next.Archived = *in.Archived
		}
		return 200, nil
	})
	s.runMu.Unlock()
	if err != nil {
		fail(w, status, err)
		return
	}
	s.readSession(w, id)
}

type sessionListOptions struct {
	query, workspace, archived string
	offset, limit              int
}

func parseSessionListOptions(r *http.Request) (sessionListOptions, error) {
	q := r.URL.Query()
	out := sessionListOptions{query: strings.TrimSpace(q.Get("q")), workspace: q.Get("workspace"), archived: q.Get("archived"), limit: 100}
	if !utf8.ValidString(out.query) || utf8.RuneCountInString(out.query) > 256 || len(out.workspace) > 128 {
		return out, fmt.Errorf("session filter exceeds its text limit")
	}
	switch out.archived {
	case "", "exclude":
		out.archived = "exclude"
	case "only", "include":
	default:
		return out, fmt.Errorf("archived must be exclude, only or include")
	}
	for key, target := range map[string]*int{"offset": &out.offset, "limit": &out.limit} {
		if text := q.Get(key); text != "" {
			n, err := strconv.Atoi(text)
			if err != nil {
				return out, fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	if out.offset < 0 || out.limit < 1 || out.limit > 200 {
		return out, fmt.Errorf("offset must be nonnegative and limit must be 1..200")
	}
	return out, nil
}
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	opts, err := parseSessionListOptions(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	summaries, err := s.sessions.List()
	if err != nil {
		fail(w, 500, err)
		return
	}
	selected := make([]store.Summary, 0)
	query := strings.ToLower(opts.query)
	for _, summary := range summaries {
		if opts.archived == "exclude" && summary.Archived || opts.archived == "only" && !summary.Archived {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(summary.Title), query) {
			continue
		}
		if opts.workspace == "unbound" {
			if summary.Workspace != "" {
				continue
			}
		} else if opts.workspace != "" && summary.Workspace != opts.workspace {
			continue
		}
		selected = append(selected, summary)
	}
	start := min(opts.offset, len(selected))
	end := min(start+opts.limit, len(selected))
	list := make([]sessionSummary, 0, end-start)
	for _, summary := range selected[start:end] {
		list = append(list, sessionSummary{ID: summary.ID, Title: summary.Title, UpdatedAt: summary.UpdatedAt, RunCount: summary.RunCount, Archived: summary.Archived, Workspace: summary.Workspace})
	}
	type workspaceOption struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	workspaces := []workspaceOption{}
	if s.workspaces != nil {
		for _, item := range s.workspaces.List() {
			workspaces = append(workspaces, workspaceOption{item.ID, item.Name})
		}
	}
	var next *int
	if end < len(selected) {
		next = &end
	}
	send(w, 200, map[string]any{"sessions": list, "total": len(selected), "offset": start, "limit": opts.limit, "next_offset": next, "workspace_options": workspaces})
}
