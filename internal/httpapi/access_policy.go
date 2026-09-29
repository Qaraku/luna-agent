package httpapi

import (
	"fmt"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/store"
)

func storedPolicy(record *store.PermissionRecord) plugin.AccessPolicy {
	return plugin.AccessPolicy{Read: plugin.AccessDecision(record.Read), Write: plugin.AccessDecision(record.Write), Network: plugin.AccessDecision(record.Network), Exec: plugin.AccessDecision(record.Exec)}
}
func policyRecord(p plugin.AccessPolicy) *store.PermissionRecord {
	return &store.PermissionRecord{Read: string(p.Read), Write: string(p.Write), Network: string(p.Network), Exec: string(p.Exec)}
}
func restrictDecision(a, b plugin.AccessDecision) plugin.AccessDecision {
	if a == plugin.DecisionDeny || b == plugin.DecisionDeny {
		return plugin.DecisionDeny
	}
	if a == plugin.DecisionAsk || b == plugin.DecisionAsk {
		return plugin.DecisionAsk
	}
	return plugin.DecisionAllow
}
func restrictPolicies(a, b plugin.AccessPolicy) plugin.AccessPolicy {
	return plugin.AccessPolicy{Read: restrictDecision(a.Read, b.Read), Write: restrictDecision(a.Write, b.Write), Network: restrictDecision(a.Network, b.Network), Exec: restrictDecision(a.Exec, b.Exec)}
}
func (s *Server) savePermissions(id string, p plugin.AccessPolicy) (executionView, int, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.busy {
		return executionView{}, 409, fmt.Errorf("stop the active run before changing permissions")
	}
	var saved store.ConfigRecord
	status, err := s.updateSessionConfig(id, func(next *store.ConfigRecord) (int, error) {
		next.Permissions = policyRecord(p)
		next.ExecutionMode = string(plugin.ExecutionSandbox)
		saved = *next
		return 200, nil
	})
	if err != nil {
		return executionView{}, status, err
	}
	if s.permissionGrants == nil {
		s.permissionGrants = make(map[string]plugin.AccessPolicy)
	}
	s.permissionGrants[id] = p
	delete(s.executionGrants, id)
	scopes, scopeErr := s.executionScopes(&saved)
	problem := ""
	if scopeErr != nil {
		problem = scopeErr.Error()
	}
	return executionView{Scopes: scopes, Problem: problem, Mode: plugin.ExecutionSandbox, Requested: plugin.ExecutionSandbox, Permissions: p, RequestedPermissions: p, GrantScope: "session_and_process"}, 200, nil
}
