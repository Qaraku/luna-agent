package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/provider"
)

// fakeProviderFile is the provider file as this layer sees it: what is stored,
// and what it was asked to write. Nothing here knows where the file lives or how
// it is written — that is the seam under test.
type fakeProviderFile struct {
	stored  provider.File
	written []provider.File
	err     error
}

func (p *fakeProviderFile) Read() (provider.File, error) {
	if p.err != nil {
		return provider.File{}, p.err
	}
	return p.stored, nil
}

func (p *fakeProviderFile) Write(file provider.File) error {
	if p.err != nil {
		return p.err
	}
	// The real file validates before it writes, and the seam is only useful if
	// the fake refuses what the real one refuses: a save that reaches the disk
	// but cannot be read back is a Luna that stops working after a restart.
	if err := file.Validate(); err != nil {
		return err
	}
	p.written = append(p.written, file)
	p.stored = file
	return nil
}

// configuredRun is the state of a process that started with a provider: the
// settings page compares what is stored against this to decide whether a restart
// is needed.
func configuredRun() Info {
	return Info{Model: "running-model", BaseURL: "https://running.example.test/v1"}
}

func handlerWithProvider(t *testing.T, config ProviderConfig, info Info) http.Handler {
	t.Helper()
	p := &fakePlugins{state: pluginState("text_transform")}
	if info.BoundHost == "" {
		info.BoundHost = "127.0.0.1:43210"
		info.WebDir = "../../web"
	}
	return New(p, fakeRunner{}, newTestStore(t), info, WithProvider(config))
}

// The key is stored, listed as set, and never sent back. A settings page that
// could read the key would put it in every screenshot and every cached response.
func TestTheProviderKeyIsNeverReturned(t *testing.T) {
	config := &fakeProviderFile{stored: provider.File{
		BaseURL: "https://api.example.test/v1", APIKey: "sk-do-not-return-me", Model: "m",
	}}
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodGet, "/api/provider", "", false)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "sk-do-not-return-me") {
		t.Fatalf("the response carries the key: %s", w.Body)
	}
	body := decodeProvider(t, w.Body.Bytes())
	if !body.KeySet {
		t.Fatal("key_set is false while a key is stored")
	}
	if body.KeyHint == "" || strings.Contains("sk-do-not-return-me", body.KeyHint) {
		t.Fatalf("key_hint = %q", body.KeyHint)
	}
	if body.BaseURL != "https://api.example.test/v1" || body.Model != "m" {
		t.Fatalf("view = %+v", body)
	}
}

// The form cannot show the key, so saving without retyping it must not erase it.
func TestSavingWithoutAKeyKeepsTheStoredOne(t *testing.T) {
	config := &fakeProviderFile{stored: provider.File{APIKey: "sk-existing"}}
	body := `{"base_url":"https://api.example.test/v1","model":"m"}`
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPut, "/api/provider", body, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(config.written) != 1 {
		t.Fatalf("wrote %d times", len(config.written))
	}
	if got := config.written[0]; got.APIKey != "sk-existing" || got.BaseURL != "https://api.example.test/v1" || got.Model != "m" {
		t.Fatalf("wrote %+v", got)
	}
}

func TestSavingAKeyReplacesTheStoredOne(t *testing.T) {
	config := &fakeProviderFile{stored: provider.File{APIKey: "sk-old"}}
	body := `{"base_url":"https://api.example.test/v1","api_key":"sk-new","model":"m"}`
	if w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPut, "/api/provider", body, true); w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if config.written[0].APIKey != "sk-new" {
		t.Fatalf("wrote %+v", config.written[0])
	}
}

// Removing a key is deliberate: it is the only way to express "this installation
// should not have one", and it must not happen by leaving a field empty.
func TestClearingTheKeyIsExplicit(t *testing.T) {
	config := &fakeProviderFile{stored: provider.File{APIKey: "sk-old"}}
	body := `{"base_url":"https://api.example.test/v1","model":"m","clear_api_key":true}`
	if w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPut, "/api/provider", body, true); w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if config.written[0].APIKey != "" {
		t.Fatalf("wrote %+v", config.written[0])
	}
}

// Saving providers a file a run calls through, so a foreign page must not be
// able to do it.
func TestSavingAProviderNeedsTheBoundOrigin(t *testing.T) {
	config := &fakeProviderFile{}
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPut, "/api/provider", `{"model":"m"}`, false)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403 without the bound Origin", w.Code)
	}
	if len(config.written) != 0 {
		t.Fatal("a request without the bound Origin wrote the file")
	}
}

// A file that cannot be used is refused with the reason, and nothing reaches the
// disk: a saved provider that cannot be read back is a Luna that stops working
// after a restart.
func TestAProviderThatCannotBeUsedIsRefused(t *testing.T) {
	config := &fakeProviderFile{}
	body := `{"base_url":"example.test/v1","model":"m"}`
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPut, "/api/provider", body, true)
	if w.Code != 400 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(config.written) != 0 {
		t.Fatalf("a refused save wrote %+v", config.written)
	}
}

// The model is built when the process starts, so a provider saved now is in
// effect from the next start. Saying so is the difference between a person
// restarting and a person believing it did not work.
func TestTheViewSaysWhetherARestartIsNeeded(t *testing.T) {
	same := &fakeProviderFile{stored: provider.File{
		BaseURL: "https://running.example.test/v1", APIKey: "k", Model: "running-model",
	}}
	w := request(t, handlerWithProvider(t, same, configuredRun()), http.MethodGet, "/api/provider", "", false)
	if got := decodeProvider(t, w.Body.Bytes()); got.RestartNeeded {
		t.Fatalf("restart_needed = true for a provider the process is already using: %+v", got)
	}

	other := &fakeProviderFile{stored: provider.File{
		BaseURL: "https://other.example.test/v1", APIKey: "k", Model: "other-model",
	}}
	w = request(t, handlerWithProvider(t, other, configuredRun()), http.MethodGet, "/api/provider", "", false)
	if got := decodeProvider(t, w.Body.Bytes()); !got.RestartNeeded {
		t.Fatalf("restart_needed = false for a provider the process is not using: %+v", got)
	}
}

// A Luna that has never been configured is the case this whole surface exists
// for, so the view has to say what is missing rather than reporting an error.
func TestAnEmptyProviderReportsWhatIsMissing(t *testing.T) {
	w := request(t, handlerWithProvider(t, &fakeProviderFile{}, Info{}), http.MethodGet, "/api/provider", "", false)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	got := decodeProvider(t, w.Body.Bytes())
	if got.Configured {
		t.Fatal("a process that started without a provider is reported as configured")
	}
	if strings.Join(got.Missing, ",") != "base_url,api_key,model" {
		t.Fatalf("missing = %v", got.Missing)
	}
	if got.KeySet || got.KeyHint != "" {
		t.Fatalf("key_set = %v, key_hint = %q", got.KeySet, got.KeyHint)
	}
}

// "What this process is using" and "what the file says" are two different
// things, and the page needs both: a provider saved while the process runs is
// complete on disk and still not in effect.
func TestTheRunningProviderAndTheStoredOneAreReportedSeparately(t *testing.T) {
	// The process started with a provider; the file no longer has one.
	w := request(t, handlerWithProvider(t, &fakeProviderFile{}, configuredRun()), http.MethodGet, "/api/provider", "", false)
	got := decodeProvider(t, w.Body.Bytes())
	if !got.Configured {
		t.Fatal("the running process is reported as unconfigured")
	}
	if len(got.Missing) == 0 || !got.RestartNeeded {
		t.Fatalf("view = %+v, want missing fields and a restart", got)
	}
}

// Asking the endpoint which models it serves is the server's call, with the
// server's key: the browser points at an endpoint, it never holds the key.
func TestProbingReportsTheModelsTheEndpointServes(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[{"id":"z"},{"id":"a"}]}`))
	}))
	defer server.Close()
	config := &fakeProviderFile{stored: provider.File{BaseURL: server.URL, APIKey: "sk-stored", Model: "m"}}
	body := `{"base_url":"` + server.URL + `"}`
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPost, "/api/provider/models", body, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if gotAuth != "Bearer sk-stored" {
		t.Fatalf("authorization = %q, want the stored key to be used when the form sends none", gotAuth)
	}
	if !strings.Contains(w.Body.String(), `"a"`) || !strings.Contains(w.Body.String(), `"z"`) {
		t.Fatalf("body = %s", w.Body)
	}
}

// A probe that fails is an answer, not a server error: the page shows the
// provider's words next to the field that caused them.
func TestAFailedProbeIsAnsweredRatherThanFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such key", http.StatusUnauthorized)
	}))
	defer server.Close()
	config := &fakeProviderFile{stored: provider.File{BaseURL: server.URL, APIKey: "sk-secret", Model: "m"}}
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPost, "/api/provider/models", `{}`, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "401") {
		t.Fatalf("the answer does not say what happened: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "sk-secret") {
		t.Fatalf("the answer leaked the key: %s", w.Body)
	}
}

// Probing spends the installation's key on an outbound call, so a foreign page
// must not be able to make Luna do it.
func TestProbingNeedsTheBoundOrigin(t *testing.T) {
	w := request(t, handlerWithProvider(t, &fakeProviderFile{}, Info{}), http.MethodPost, "/api/provider/models", `{}`, false)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403 without the bound Origin", w.Code)
	}
}

func TestProviderMethodsAreEnforced(t *testing.T) {
	h := handlerWithProvider(t, &fakeProviderFile{}, Info{})
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/provider"},
		{http.MethodDelete, "/api/provider"},
		{http.MethodGet, "/api/provider/models"},
	} {
		w := request(t, h, tc.method, tc.path, "", true)
		if w.Code != 405 {
			t.Fatalf("%s %s: status = %d, want 405", tc.method, tc.path, w.Code)
		}
	}
}

// A storage failure is reported as one: the page must not tell a person their
// provider was saved when it was not.
func TestAStorageFailureIsReported(t *testing.T) {
	config := &fakeProviderFile{err: errors.New("disk is full")}
	w := request(t, handlerWithProvider(t, config, Info{}), http.MethodPut, "/api/provider", `{"model":"m"}`, true)
	if w.Code != 500 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
}

func decodeProvider(t *testing.T, data []byte) providerView {
	t.Helper()
	var view providerView
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return view
}
