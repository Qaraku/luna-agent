package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListModelsReadsTheIDList(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"b-model"},{"id":"a-model"},{"id":"a-model"},{"id":"  "}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), server.URL+"/v1", "sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	// The list is sorted and deduplicated so two runs against the same endpoint
	// produce the same candidates in the same order.
	if strings.Join(models, ",") != "a-model,b-model" {
		t.Fatalf("models = %v", models)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q, want the base URL's own path plus /models", gotPath)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Fatalf("authorization = %q", gotAuth)
	}
}

// A base URL that already ends in /v1 must not become /v1/v1/models, and one
// written with a trailing slash must not become //models.
func TestListModelsJoinsTheEndpointOnce(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer server.Close()
	for _, base := range []string{server.URL, server.URL + "/", server.URL + "/v1", server.URL + "/v1/"} {
		if _, err := ListModels(context.Background(), base, "k"); err != nil {
			t.Fatalf("%q: %v", base, err)
		}
		want := strings.TrimRight(strings.TrimPrefix(base, server.URL), "/") + "/models"
		if path != want {
			t.Fatalf("base %q asked %q, want %q", base, path, want)
		}
	}
}

// The key is sent on the request and never into the error: a probe failure is
// shown in a browser, and an error is the one place a secret leaks by accident.
func TestListModelsNeverPutsTheKeyInAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream said no", http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := ListModels(context.Background(), server.URL, "sk-top-secret")
	if err == nil {
		t.Fatal("a 401 was accepted")
	}
	if strings.Contains(err.Error(), "sk-top-secret") {
		t.Fatalf("the error leaked the key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("the error does not say what the endpoint answered: %v", err)
	}
}

func TestListModelsReportsAnEndpointThatIsNotAList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>welcome to my homepage</html>"))
	}))
	defer server.Close()
	if _, err := ListModels(context.Background(), server.URL, "k"); err == nil {
		t.Fatal("an HTML page was accepted as a model list")
	}
}

// An endpoint that answers with an empty list is a real answer, and it means
// something a person needs to read: the endpoint or the key is wrong.
func TestListModelsTreatsAnEmptyListAsAFailureWithAReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	if _, err := ListModels(context.Background(), server.URL, "k"); err == nil {
		t.Fatal("an empty list was accepted as a set of candidates")
	}
}

func TestListModelsNeedsAnEndpoint(t *testing.T) {
	if _, err := ListModels(context.Background(), "  ", "k"); err == nil {
		t.Fatal("an empty base URL was accepted")
	}
}

// A provider that never answers must not hold the request open: the caller sets
// the deadline, and it has to be the caller's to set.
func TestListModelsHonoursTheContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ListModels(ctx, server.URL, "k"); err == nil {
		t.Fatal("a request that never answers was accepted")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the deadline was ignored: %v", elapsed)
	}
}

// A response larger than any model list is cut off instead of being read whole:
// a wrong URL must not decide how much memory this process spends.
func TestListModelsBoundsWhatItReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"`))
		chunk := strings.Repeat("x", 4096)
		for written := 0; written < maxModelsResponse+len(chunk); written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	if _, err := ListModels(context.Background(), server.URL, "k"); err == nil {
		t.Fatal("an oversized response was accepted")
	}
}
