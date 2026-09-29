package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestVersionHasBuildIdentityWithoutPrivateConfiguration(t *testing.T) {
	handler := handlerWithStore(t, nil, newTestStore(t))
	response := request(t, handler, http.MethodGet, "/api/version", "", false)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["data_schema"] != float64(1) || body["version"] == "" {
		t.Fatal(body)
	}
	for _, key := range []string{"config", "api_key", "paths", "sessions"} {
		if _, ok := body[key]; ok {
			t.Fatal("private data exposed", key)
		}
	}
	if w := request(t, handler, http.MethodPost, "/api/version", "{}", true); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
