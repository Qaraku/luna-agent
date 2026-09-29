package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 审批只存在于正在运行的服务内存中；HTTP 决策只携带身份和同意/拒绝，不接收参数。
type approvalView struct {
	ID        string               `json:"id"`
	RunID     string               `json:"run_id"`
	SessionID string               `json:"session_id"`
	CreatedAt time.Time            `json:"created_at"`
	ExpiresAt time.Time            `json:"expires_at,omitempty"`
	Operation plugin.AccessRequest `json:"operation"`
}
type pendingApproval struct {
	view     approvalView
	ctx      context.Context
	decision chan bool
}

func approvalPath(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, "/api/approvals/")
	return id, ok && id != "" && !strings.Contains(id, "/")
}
func (s *Server) awaitApproval(ctx context.Context, runID, sessionID string, operation plugin.AccessRequest, sink agent.Sink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return plugin.Unavailable(fmt.Errorf("cannot allocate approval identity"))
	}
	view := approvalView{ID: hex.EncodeToString(entropy[:]), RunID: runID, SessionID: sessionID, CreatedAt: time.Now(), Operation: operation}
	if deadline, ok := ctx.Deadline(); ok {
		view.ExpiresAt = deadline
	}
	pending := &pendingApproval{view: view, ctx: ctx, decision: make(chan bool, 1)}
	s.runMu.Lock()
	if !s.busy || s.runID != runID || s.sessionID != sessionID {
		s.runMu.Unlock()
		return fmt.Errorf("approval no longer belongs to an active run")
	}
	if s.pendingApprovals == nil {
		s.pendingApprovals = make(map[string]*pendingApproval)
	}
	if len(s.pendingApprovals) >= 16 {
		s.runMu.Unlock()
		return fmt.Errorf("too many pending approvals")
	}
	s.pendingApprovals[view.ID] = pending
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		if s.pendingApprovals[view.ID] == pending {
			delete(s.pendingApprovals, view.ID)
		}
		s.runMu.Unlock()
	}()
	if sink != nil {
		sink.Emit(agent.Event{Type: "approval.requested", Data: view})
	}
	decision := "cancelled"
	defer func() {
		if sink != nil {
			sink.Emit(agent.Event{Type: "approval.resolved", Data: map[string]string{"id": view.ID, "run_id": runID, "session_id": sessionID, "decision": decision}})
		}
	}()
	select {
	case approved := <-pending.decision:
		if err := ctx.Err(); err != nil {
			return err
		}
		if !approved {
			decision = "deny"
			return plugin.ErrAccessDenied
		}
		decision = "approve"
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("session"))
	if id != "" {
		if _, err := s.sessions.Read(id); err != nil {
			fail(w, sessionStatus(err), err)
			return
		}
	}
	list := make([]approvalView, 0)
	s.runMu.Lock()
	for _, pending := range s.pendingApprovals {
		if id != "" && pending.view.SessionID == id && pending.ctx.Err() == nil && s.busy && s.runID == pending.view.RunID {
			list = append(list, pending.view)
		}
	}
	s.runMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	send(w, 200, struct {
		Approvals []approvalView `json:"approvals"`
	}{list})
}
func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
		Decision  string `json:"decision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Decision != "approve" && in.Decision != "deny" {
		fail(w, 400, fmt.Errorf("decision must be approve or deny"))
		return
	}
	s.runMu.Lock()
	pending, ok := s.pendingApprovals[id]
	if !ok {
		s.runMu.Unlock()
		fail(w, 404, fmt.Errorf("no pending approval"))
		return
	}
	if pending.view.RunID != in.RunID || pending.view.SessionID != in.SessionID {
		s.runMu.Unlock()
		fail(w, 409, fmt.Errorf("approval identity does not match this run and session"))
		return
	}
	if !s.busy || s.runID != in.RunID || s.sessionID != in.SessionID || pending.ctx.Err() != nil {
		delete(s.pendingApprovals, id)
		s.runMu.Unlock()
		fail(w, 409, fmt.Errorf("approval has expired or the run ended"))
		return
	}
	delete(s.pendingApprovals, id)
	pending.decision <- in.Decision == "approve"
	s.runMu.Unlock()
	send(w, 200, map[string]string{"id": id, "decision": in.Decision})
}
