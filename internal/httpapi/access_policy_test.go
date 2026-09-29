package httpapi

import (
	"encoding/json"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/store"
	"net/http"
	"testing"
)

func permissionBody(p plugin.AccessPolicy) map[string]any {
	return map[string]any{"permissions": p, "confirm_permissions": true}
}
func permissionsView(t *testing.T, h http.Handler, id string) executionView {
	t.Helper()
	r := request(t, h, http.MethodGet, "/api/execution?session="+id, "", false)
	if r.Code != 200 {
		t.Fatal(r.Body)
	}
	var view executionView
	if err := json.Unmarshal(r.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}
func TestAccessPolicySaveRequiresConfirmationAndKeepsOtherSettings(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("policy")
	h := executionHandler(sessions, fakeRunner{})
	if err := sessions.AppendConfig(id, store.ConfigRecord{Model: "selected", Workspace: "project"}); err != nil {
		t.Fatal(err)
	}
	p := plugin.AccessPolicy{Read: plugin.DecisionAllow, Write: plugin.DecisionAsk, Network: plugin.DecisionDeny, Exec: plugin.DecisionAllow}
	endpoint := "/api/sessions/" + id + "/execution"
	if got := controlsRequest(t, h, endpoint, map[string]any{"permissions": p}); got.Code != 400 {
		t.Fatalf("unconfirmed=%d", got.Code)
	}
	if got := controlsRequest(t, h, endpoint, permissionBody(p)); got.Code != 200 {
		t.Fatalf("save=%d %s", got.Code, got.Body)
	}
	view := permissionsView(t, h, id)
	if view.Permissions != p || view.PermissionsNeedConfirmation {
		t.Fatalf("view=%+v", view)
	}
	session, _ := sessions.Read(id)
	if session.Config.Model != "selected" || session.Config.Workspace != "project" || session.Config.Permissions == nil {
		t.Fatalf("config=%+v", session.Config)
	}
}
func TestAccessPolicyPersistenceNeverGrantsAfterRestart(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("policy")
	h := executionHandler(sessions, fakeRunner{})
	p := plugin.AccessPolicy{Read: plugin.DecisionAllow, Write: plugin.DecisionAllow, Network: plugin.DecisionAllow, Exec: plugin.DecisionAllow}
	if got := controlsRequest(t, h, "/api/sessions/"+id+"/execution", permissionBody(p)); got.Code != 200 {
		t.Fatal(got.Body)
	}
	restarted := executionHandler(sessions, fakeRunner{})
	view := permissionsView(t, restarted, id)
	if view.Permissions != plugin.DefaultAccessPolicy() || !view.PermissionsNeedConfirmation {
		t.Fatalf("restored=%+v", view)
	}
	copied, _ := sessions.Create("copy")
	original, _ := sessions.Read(id)
	if err := sessions.AppendConfig(copied, *original.Config); err != nil {
		t.Fatal(err)
	}
	if got := permissionsView(t, h, copied); got.Permissions != plugin.DefaultAccessPolicy() {
		t.Fatalf("copied=%+v", got)
	}
}
func TestAccessPolicyRejectsIncompleteOrUnknownMatrix(t *testing.T) {
	sessions := newTestStore(t)
	id, _ := sessions.Create("policy")
	h := executionHandler(sessions, fakeRunner{})
	for _, p := range []map[string]any{{"read": "allow"}, {"read": "allow", "write": "allow", "network": "anything", "exec": "allow"}} {
		if got := controlsRequest(t, h, "/api/sessions/"+id+"/execution", map[string]any{"permissions": p, "confirm_permissions": true}); got.Code != 400 {
			t.Fatalf("invalid matrix=%d", got.Code)
		}
	}
	if got := permissionsView(t, h, id); got.Permissions != plugin.DefaultAccessPolicy() {
		t.Fatalf("default=%+v", got)
	}
}
