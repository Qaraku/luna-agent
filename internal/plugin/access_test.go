package plugin

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func accessContext(policy AccessPolicy, approve ApprovalFunc) context.Context {
	return WithRun(context.Background(), RunInfo{RunID: "run", SessionID: "session", Permissions: &policy, Approve: approve})
}
func TestAccessDefaultsRequireApprovalExceptProjectRead(t *testing.T) {
	if got := DefaultAccessPolicy(); got != (AccessPolicy{Read: DecisionAllow, Write: DecisionAsk, Network: DecisionAsk, Exec: DecisionAsk}) {
		t.Fatalf("defaults: %+v", got)
	}
	if err := RequireAccess(context.Background(), AccessRequest{Tool: "read", Permissions: []AccessKind{AccessRead}}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []AccessKind{AccessWrite, AccessNetwork, AccessExec} {
		if err := RequireAccess(context.Background(), AccessRequest{Tool: "test", Permissions: []AccessKind{kind}}); !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
func TestAccessCombinesApprovalAndNeverApprovesDeniedDimension(t *testing.T) {
	policy := DefaultAccessPolicy()
	calls := 0
	approve := func(_ context.Context, r AccessRequest) error {
		calls++
		if !reflect.DeepEqual(r.Ask, []AccessKind{AccessWrite, AccessNetwork, AccessExec}) {
			t.Fatalf("ask: %v", r.Ask)
		}
		return nil
	}
	request := AccessRequest{Tool: "shell", Permissions: []AccessKind{AccessExec, AccessWrite, AccessNetwork, AccessWrite, AccessRead}}
	if err := RequireAccess(accessContext(policy, approve), request); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("approval calls: %d", calls)
	}
	policy.Network = DecisionDeny
	if err := RequireAccess(accessContext(policy, approve), request); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("deny: %v", err)
	}
	if calls != 1 {
		t.Fatal("denied operation reached approval")
	}
}
func TestAccessExplicitAllowScopeAndCancellation(t *testing.T) {
	policy := AccessPolicy{Read: DecisionAllow, Write: DecisionAllow, Network: DecisionDeny, Exec: DecisionAllow}
	request := AccessRequest{Tool: "write", Permissions: []AccessKind{AccessWrite}}
	if err := RequireAccess(accessContext(policy, nil), request); err != nil {
		t.Fatal(err)
	}
	request.ScopeApproval = true
	if err := RequireAccess(accessContext(policy, nil), request); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("unapproved scope: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithRun(ctx, RunInfo{Permissions: &policy, Approve: func(context.Context, AccessRequest) error { cancel(); return nil }})
	if err := RequireAccess(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during approval: %v", err)
	}
}
func TestAccessSavedAllowsArePreferencesNotRestartGrants(t *testing.T) {
	p := AccessPolicy{Read: DecisionDeny, Write: DecisionAllow, Network: DecisionDeny, Exec: DecisionAllow}
	got := p.WithoutElevatedGrants()
	if got.Read != DecisionDeny || got.Write != DecisionAsk || got.Network != DecisionDeny || got.Exec != DecisionAsk {
		t.Fatalf("restored: %+v", got)
	}
	p.Exec = "unknown"
	if p.Valid() {
		t.Fatal("unknown decision accepted")
	}
	if err := RequireAccess(accessContext(p, nil), AccessRequest{Tool: "x", Permissions: []AccessKind{AccessExec}}); err == nil {
		t.Fatal("invalid policy served")
	}
}
func TestFullAccessIsExplicitExceptionNotAnArgument(t *testing.T) {
	p := AccessPolicy{Read: DecisionDeny, Write: DecisionDeny, Network: DecisionDeny, Exec: DecisionDeny}
	ctx := WithRun(context.Background(), RunInfo{ExecutionMode: ExecutionFullAccess, Permissions: &p})
	if err := RequireAccess(ctx, AccessRequest{Tool: "shell", Permissions: []AccessKind{AccessExec, AccessWrite, AccessNetwork}}); err != nil {
		t.Fatal(err)
	}
	if err := RequireAccess(ctx, AccessRequest{Tool: "bad", Permissions: []AccessKind{"unknown"}}); err == nil {
		t.Fatal("unknown dimension accepted")
	}
}
