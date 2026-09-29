package httpapi

import (
	"fmt"
	"net/http"

	"github.com/Qaraku/luna-agent/internal/store"
)

// createSession 为第一条消息之前的运行设置分配持久身份，不调用模型或写入消息。
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var in *struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in == nil {
		fail(w, 400, fmt.Errorf("a session creation request must be a JSON object"))
		return
	}
	id, err := s.sessions.Create(in.Title)
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	session, err := s.sessions.Read(id)
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	records := session.Records
	if records == nil {
		records = []store.Record{}
	}
	send(w, http.StatusCreated, sessionDetail{ID: session.ID, Title: session.Title, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt, RunCount: session.RunCount, Truncated: session.Truncated, Workspace: s.sessionWorkspace(session), Records: records})
}
