package sessionhistory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/store"
)

type searchSpy struct {
	calls   int
	options store.SearchOptions
}

func (s *searchSpy) Dir() string { return "/private/sessions" }
func (s *searchSpy) Search(ctx context.Context, opts store.SearchOptions) (store.SearchResult, error) {
	s.calls++
	s.options = opts
	return store.SearchResult{Matches: []store.SearchMatch{}, Limits: []string{}}, nil
}
func TestHistoryUsesHostWorkspaceAndApprovesWideningBeforeReads(t *testing.T) {
	source := &searchSpy{}
	p := New(source)
	tool := p.Tools()[0]
	approved := 0
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run", WorkspaceID: "project-a", Approve: func(context.Context, plugin.AccessRequest) error {
		approved++
		if source.calls != 1 {
			t.Fatal("broad read preceded approval")
		}
		return errors.New("declined")
	}})
	if _, err := tool.Invoke(ctx, `{"query":"topic"}`); err != nil {
		t.Fatal(err)
	}
	if source.options.Workspace == nil || *source.options.Workspace != "project-a" || approved != 0 {
		t.Fatal("did not use host scope")
	}
	if _, err := tool.Invoke(ctx, `{"query":"topic","scope":"all"}`); err == nil {
		t.Fatal("rejected broad search ran")
	}
	if source.calls != 1 || approved != 1 {
		t.Fatal("unexpected calls")
	}
	ctx = plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run", WorkspaceID: "project-a", Approve: func(_ context.Context, r plugin.AccessRequest) error {
		if !r.ScopeApproval {
			t.Fatal("scope approval missing")
		}
		return nil
	}})
	out, err := tool.Invoke(ctx, `{"query":"topic","scope":"all"}`)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if json.Unmarshal([]byte(out), &payload) != nil || source.options.Workspace != nil {
		t.Fatal("all scope not applied")
	}
}
func TestHistoryReadDenyAndForgedWorkspaceNeverReachStore(t *testing.T) {
	source := &searchSpy{}
	tool := New(source).Tools()[0]
	policy := plugin.DefaultAccessPolicy()
	policy.Read = plugin.DecisionDeny
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", Permissions: &policy})
	if _, err := tool.Invoke(ctx, `{"query":"topic"}`); !errors.Is(err, plugin.ErrAccessDenied) {
		t.Fatalf("read denial=%v", err)
	}
	ctx = plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa"})
	if _, err := tool.Invoke(ctx, `{"query":"topic","workspace_id":"other"}`); err == nil {
		t.Fatal("accepted model-selected project")
	}
	if source.calls != 0 {
		t.Fatal("invalid request reached store")
	}
}
