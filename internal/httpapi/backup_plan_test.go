package httpapi

import (
	"encoding/json"
	"github.com/Qaraku/luna-agent/internal/privatebackup"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackupPlanIsReadOnlyAndContainsLocationsNotSecrets(t *testing.T) {
	p := privatebackup.Plan{Format: 1, ConfigDir: "/fixture/config", ConfigFile: "/fixture/config/config.yaml", DataDir: "/fixture/data", StateDir: "/fixture/data", SessionsDir: "/fixture/data/sessions"}
	server := New(nil, nil, newTestStore(t), Info{BoundHost: "127.0.0.1:3210", DataPlan: &p})
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:3210/api/backup-plan", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got privatebackup.Plan
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionsDir != p.SessionsDir || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("plan not attached safely", got, w.Header())
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3210/api/backup-plan", nil)
	request.Header.Set("Origin", "http://127.0.0.1:3210")
	w = httptest.NewRecorder()
	server.ServeHTTP(w, request)
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
