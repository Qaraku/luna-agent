package httpapi

import (
	"context"
	"encoding/json"
	"luna-plugin-demo/internal/kernel"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPContractAndGuards(t *testing.T) {
	root, _ := filepath.Abs("../..")
	k, err := kernel.New(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>local fixture</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	h := New(k, "127.0.0.1:12345", web)
	cases := []struct {
		method, path, body, origin, host string
		status                           int
	}{
		{"GET", "/api/state", "", "", "", 200}, {"POST", "/api/invoke", `{"text":" hi "}`, "", "", 200},
		{"GET", "/api/invoke", "", "", "", 405}, {"POST", "/api/invoke", `{"text":"hi"}`, "https://evil.test", "", 403},
		{"POST", "/api/invoke", `{}`, "null", "", 403}, {"POST", "/api/invoke", `{}`, "", "evil.test:12345", 403},
		{"POST", "/api/invoke", strings.Repeat("x", 33000), "", "", 413}, {"POST", "/api/invoke", `{"delay_ms":3001}`, "", "", 400},
		{"POST", "/api/reload", `{"candidate":"../../etc"}`, "", "", 400}, {"POST", "/api/invoke", `{} {}`, "", "", 400},
		{"POST", "/api/invoke", `{"unknown":1}`, "", "", 400}, {"GET", "/healthz", "", "", "", 200}, {"GET", "/", "", "", "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path+tc.origin+tc.host+tc.body[:min(12, len(tc.body))], func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:12345"+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.host != "" {
				r.Host = tc.host
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.path == "/" && !strings.Contains(w.Body.String(), "<!doctype html>") {
				t.Fatal("HTML not served")
			}
			if tc.path == "/api/state" && w.Code == 200 {
				var s kernel.State
				if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || s.Active.PluginPID <= 0 || !s.Demo {
					t.Fatalf("bad state: %s", w.Body.String())
				}
			}
		})
	}
}
func TestLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "example.com:0"} {
		if _, err := Listen(addr); err == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	_ = http.MethodGet
}
