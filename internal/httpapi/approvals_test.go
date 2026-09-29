package httpapi

import (
	"context"
	"errors"
	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"net/http"
	"strings"
	"testing"
	"time"
)

type approvalSink chan agent.Event

func (s approvalSink) Emit(e agent.Event) { s <- e }
func approvalFixture(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	sessions := newTestStore(t)
	id, err := sessions.Create("approval")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{sessions: sessions, info: Info{BoundHost: "127.0.0.1:43210"}, busy: true, runID: "run-approval", sessionID: id}
	return s, http.HandlerFunc(s.serveHTTP), id
}
func startPendingApproval(t *testing.T, s *Server, session string, ctx context.Context) (approvalView, <-chan error) {
	t.Helper()
	sink := make(approvalSink, 4)
	done := make(chan error, 1)
	go func() {
		done <- s.awaitApproval(ctx, "run-approval", session, plugin.AccessRequest{Tool: "fixture", Summary: "write one file", Target: "/project/file", ParametersDigest: "exact-parameters", Permissions: []plugin.AccessKind{plugin.AccessWrite}, Ask: []plugin.AccessKind{plugin.AccessWrite}}, sink)
	}()
	select {
	case event := <-sink:
		if event.Type != "approval.requested" {
			t.Fatalf("event=%s", event.Type)
		}
		return event.Data.(approvalView), done
	case <-time.After(time.Second):
		t.Fatal("approval event absent")
		return approvalView{}, done
	}
}
func TestApprovalBlocksAndRequiresOriginAndExactIdentity(t *testing.T) {
	s, h, id := approvalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending, done := startPendingApproval(t, s, id, ctx)
	select {
	case err := <-done:
		t.Fatalf("operation resumed before approval: %v", err)
	default:
	}
	endpoint := "/api/approvals/" + pending.ID
	body := map[string]string{"run_id": "run-approval", "session_id": id, "decision": "approve"}
	if got := request(t, h, http.MethodPost, endpoint,
		`{"run_id":"run-approval","session_id":"`+id+`","decision":"approve"}`, false); got.Code != 403 {
		t.Fatalf("originless decision=%d", got.Code)
	}
	for _, bad := range []map[string]string{{"run_id": "other", "session_id": id, "decision": "approve"}, {"run_id": "run-approval", "session_id": "other", "decision": "approve"}, {"run_id": "run-approval", "session_id": id, "decision": "allow-all"}} {
		if got := controlsRequest(t, h, endpoint, bad); got.Code < 400 {
			t.Fatalf("bad decision=%d", got.Code)
		}
	}
	if got := controlsRequest(t, h, endpoint, body); got.Code != 200 {
		t.Fatalf("decision=%d %s", got.Code, got.Body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("approved call did not resume")
	}
	if got := controlsRequest(t, h, endpoint, body); got.Code != 404 {
		t.Fatalf("decision reused=%d", got.Code)
	}
}
func TestApprovalDenyCancelAndListAreSessionBound(t *testing.T) {
	for _, action := range []string{"deny", "cancel"} {
		t.Run(action, func(t *testing.T) {
			s, h, id := approvalFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pending, done := startPendingApproval(t, s, id, ctx)
			list := request(t, h, http.MethodGet, "/api/approvals?session="+id, "", false)
			if list.Code != 200 || !strings.Contains(list.Body.String(), pending.ID) {
				t.Fatalf("list: %d %s", list.Code, list.Body)
			}
			other, _ := s.sessions.Create("other")
			list = request(t, h, http.MethodGet, "/api/approvals?session="+other, "", false)
			if strings.Contains(list.Body.String(), pending.ID) {
				t.Fatal("approval leaked across sessions")
			}
			if action == "cancel" {
				cancel()
			} else {
				if got := controlsRequest(t, h, "/api/approvals/"+pending.ID, map[string]string{"run_id": "run-approval", "session_id": id, "decision": "deny"}); got.Code != 200 {
					t.Fatal(got.Body)
				}
			}
			select {
			case err := <-done:
				if action == "deny" && !errors.Is(err, plugin.ErrAccessDenied) {
					t.Fatalf("deny: %v", err)
				}
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending call did not stop")
			}
			list = request(t, h, http.MethodGet, "/api/approvals?session="+id, "", false)
			if strings.Contains(list.Body.String(), pending.ID) {
				t.Fatal("resolved approval remains visible")
			}
		})
	}
}

func TestApprovalDeadlineExpiresWithoutDecision(t *testing.T) {
	s, h, id := approvalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pending, done := startPendingApproval(t, s, id, ctx)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expiry=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("approval did not expire")
	}
	if got := controlsRequest(t, h, "/api/approvals/"+pending.ID, map[string]string{"session_id": id, "run_id": "run-approval", "decision": "approve"}); got.Code != 404 {
		t.Fatalf("expired approval accepted: %d", got.Code)
	}
}
