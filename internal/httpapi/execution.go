package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/store"
)

type executionView struct {
	Scopes                      executionScopes `json:"scopes"`
	config                      *store.ConfigRecord
	Permissions                 plugin.AccessPolicy  `json:"permissions"`
	RequestedPermissions        plugin.AccessPolicy  `json:"requested_permissions"`
	PermissionsNeedConfirmation bool                 `json:"permissions_need_confirmation"`
	Mode                        plugin.ExecutionMode `json:"mode"`
	Requested                   plugin.ExecutionMode `json:"requested_mode"`
	NeedsConfirmation           bool                 `json:"needs_confirmation"`
	GrantScope                  string               `json:"grant_scope"`
	Problem                     string               `json:"problem,omitempty"`
}

// executionViewLocked 必须持有 runMu。持久化值只表示偏好，授权来自当前 Server 内存。
func (s *Server) executionViewLocked(id string) (executionView, error) {
	if s.busy && s.sessionID == id && s.activeExecution != nil {
		return *s.activeExecution, nil
	}
	view := executionView{Mode: plugin.ExecutionSandbox, Requested: plugin.ExecutionSandbox, GrantScope: "session_and_process", Permissions: plugin.DefaultAccessPolicy(), RequestedPermissions: plugin.DefaultAccessPolicy()}
	if id == "" {
		view.Scopes, _ = s.executionScopes(nil)
		return view, nil
	}
	session, err := s.sessions.Read(id)
	if err != nil {
		return view, err
	}
	view.config = session.Config
	view.Scopes, err = s.executionScopes(session.Config)
	if err != nil {
		view.Problem = err.Error()
	}
	if session.Config != nil && session.Config.Permissions != nil {
		view.RequestedPermissions = storedPolicy(session.Config.Permissions)
		if !view.RequestedPermissions.Valid() {
			view.Problem = "stored permissions are invalid; choose a valid permission matrix"
			return view, nil
		}
	}
	view.Permissions = view.RequestedPermissions.WithoutElevatedGrants()
	if granted, ok := s.permissionGrants[id]; ok {
		if granted == view.RequestedPermissions {
			view.Permissions = granted
		} else {
			view.Permissions = restrictPolicies(granted, view.Permissions)
		}
	}
	view.PermissionsNeedConfirmation = view.Permissions != view.RequestedPermissions
	if session.Config != nil && session.Config.ExecutionMode != "" {
		view.Requested = plugin.ExecutionMode(session.Config.ExecutionMode)
	}
	if !view.Requested.Valid() {
		view.Requested = ""
		view.Problem = "stored execution mode is invalid; choose sandbox or confirm full access"
		return view, nil
	}
	if view.Requested == plugin.ExecutionFullAccess {
		if s.executionGrants[id] {
			view.Permissions = plugin.AccessPolicy{Read: plugin.DecisionAllow, Write: plugin.DecisionAllow, Network: plugin.DecisionAllow, Exec: plugin.DecisionAllow}
			view.PermissionsNeedConfirmation = false
			view.Mode = plugin.ExecutionFullAccess
			view.Scopes.Network = "host"
		} else {
			view.NeedsConfirmation = true
		}
	}
	return view, nil
}
func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	s.runMu.Lock()
	view, err := s.executionViewLocked(strings.TrimSpace(r.URL.Query().Get("session")))
	s.runMu.Unlock()
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	send(w, 200, view)
}
func sessionExecutionPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/api/sessions/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/execution")
	return id, ok && id != ""
}
func (s *Server) setExecution(w http.ResponseWriter, r *http.Request, id string) {
	var in *struct {
		Mode               plugin.ExecutionMode `json:"mode"`
		Confirm            bool                 `json:"confirm_full_access"`
		Permissions        *plugin.AccessPolicy `json:"permissions"`
		ConfirmPermissions bool                 `json:"confirm_permissions"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in != nil && in.Permissions != nil {
		if !in.Permissions.Valid() || !in.ConfirmPermissions || (in.Mode != "" && in.Mode != plugin.ExecutionSandbox) {
			fail(w, 400, fmt.Errorf("a complete permission matrix and confirm_permissions=true are required; full access is a separate choice"))
			return
		}
		view, status, err := s.savePermissions(id, *in.Permissions)
		if err != nil {
			fail(w, status, err)
			return
		}
		send(w, 200, view)
		return
	}
	if in == nil || !in.Mode.Valid() {
		fail(w, 400, fmt.Errorf("mode must be sandbox or full_access"))
		return
	}
	if in.Mode == plugin.ExecutionFullAccess && !in.Confirm {
		fail(w, 400, fmt.Errorf("full access requires explicit confirmation"))
		return
	}
	view, status, err := s.saveExecution(id, in.Mode)
	if err != nil {
		fail(w, status, err)
		return
	}
	s.addEvent("execution_permission_changed", "session "+id+" execution permission updated")
	send(w, 200, view)
}
func (s *Server) saveExecution(id string, mode plugin.ExecutionMode) (executionView, int, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.busy {
		return executionView{}, 409, fmt.Errorf("stop the active run before changing execution permissions")
	}
	requested := plugin.DefaultAccessPolicy()
	var saved store.ConfigRecord
	status, err := s.updateSessionConfig(id, func(next *store.ConfigRecord) (int, error) {
		if next.Permissions != nil {
			requested = storedPolicy(next.Permissions)
			if !requested.Valid() {
				return 400, fmt.Errorf("stored permissions are invalid; choose a valid permission matrix")
			}
		}
		next.ExecutionMode = string(mode)
		saved = *next
		return 200, nil
	})
	if err != nil {
		return executionView{}, status, err
	}
	if mode == plugin.ExecutionFullAccess {
		if s.executionGrants == nil {
			s.executionGrants = make(map[string]bool)
		}
		s.executionGrants[id] = true
	} else {
		delete(s.executionGrants, id)
	}
	effective := requested.WithoutElevatedGrants()
	if granted, ok := s.permissionGrants[id]; ok {
		if granted == requested {
			effective = granted
		} else {
			effective = restrictPolicies(granted, effective)
		}
	}
	if mode == plugin.ExecutionFullAccess {
		effective = plugin.AccessPolicy{Read: plugin.DecisionAllow, Write: plugin.DecisionAllow, Network: plugin.DecisionAllow, Exec: plugin.DecisionAllow}
	}
	scopes, scopeErr := s.executionScopes(&saved)
	problem := ""
	if scopeErr != nil {
		problem = scopeErr.Error()
	}
	if mode == plugin.ExecutionFullAccess {
		scopes.Network = "host"
	}
	return executionView{Scopes: scopes, Problem: problem, Mode: mode, Requested: mode, GrantScope: "session_and_process", Permissions: effective, RequestedPermissions: requested, PermissionsNeedConfirmation: mode != plugin.ExecutionFullAccess && effective != requested}, 200, nil
}
