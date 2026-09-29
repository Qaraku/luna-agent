package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/config"
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
	// but cannot be read back is a Luna that stops working on the next run.
	if err := file.Validate(); err != nil {
		return err
	}
	p.written = append(p.written, file)
	p.stored = file
	return nil
}

// fakeConfigSource is what a run started now would use, as the interface reads it.
// It is mutable so a test can change the provider between two requests, which is
// what "saved while the process runs" looks like from here.
type fakeConfigSource struct {
	cfg config.Config
	err error
}

func (s *fakeConfigSource) Current() (config.Config, error) {
	if s.err != nil {
		return config.Config{}, s.err
	}
	return s.cfg, nil
}

func handlerWithProvider(t *testing.T, stored ProviderConfig, info Info, opts ...Option) http.Handler {
	t.Helper()
	p := &fakePlugins{state: pluginState("text_transform")}
	if info.BoundHost == "" {
		info.BoundHost = "127.0.0.1:43210"
		info.WebDir = "../../web"
	}
	return New(p, fakeRunner{}, newTestStore(t), info, append([]Option{WithProvider(stored)}, opts...)...)
}

// oneProvider is the body a settings page submits for a single complete provider.
func oneProvider(name, baseURL, key, model string) map[string]any {
	return map[string]any{
		"active": name,
		"providers": []map[string]any{
			{"name": name, "base_url": baseURL, "api_key": key, "model": model, "models": []string{}},
		},
	}
}

func putProvider(t *testing.T, h http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return request(t, h, http.MethodPut, "/api/provider", string(data), true)
}

// The key is stored, listed as set, and never sent back. A settings page that
// could read the key would put it in every screenshot and every cached response.
func TestTheProviderKeyIsNeverReturned(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active: "alpha",
		Providers: map[string]provider.Endpoint{
			"alpha": {BaseURL: "https://api.example.test/v1", APIKey: "sk-do-not-return-me", Model: "m"},
		},
	}}
	w := request(t, handlerWithProvider(t, stored, Info{}), http.MethodGet, "/api/provider", "", false)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "sk-do-not-return-me") {
		t.Fatalf("the response carries the key: %s", w.Body)
	}
	view := decodeProvider(t, w.Body.Bytes())
	if len(view.Providers) != 1 {
		t.Fatalf("providers = %+v", view.Providers)
	}
	only := view.Providers[0]
	if !only.KeySet || only.KeyHint == "" || strings.Contains("sk-do-not-return-me", only.KeyHint) {
		t.Fatalf("key_set = %v, key_hint = %q", only.KeySet, only.KeyHint)
	}
	if only.Name != "alpha" || only.BaseURL != "https://api.example.test/v1" || only.Model != "m" {
		t.Fatalf("provider = %+v", only)
	}
	if view.Active != "alpha" || !view.Configured || len(view.Missing) != 0 {
		t.Fatalf("view = %+v", view)
	}
}

// A provider is a named thing: the interface shows the names, and two of them are
// listed and ordered the same way on every read.
func TestTheProvidersAreListedByName(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active: "beta",
		Providers: map[string]provider.Endpoint{
			"beta":  {BaseURL: "https://beta.example.test/v1", APIKey: "k", Model: "b"},
			"alpha": {BaseURL: "https://alpha.example.test/v1", APIKey: "k", Model: "a", Models: []string{"a2"}},
		},
	}}
	w := request(t, handlerWithProvider(t, stored, Info{}), http.MethodGet, "/api/provider", "", false)
	view := decodeProvider(t, w.Body.Bytes())
	names := []string{view.Providers[0].Name, view.Providers[1].Name}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("providers are not in name order: %v", names)
	}
	if got := view.Providers[0].Models; len(got) != 1 || got[0] != "a2" {
		t.Fatalf("models = %v, want the extra model of alpha", got)
	}
	if view.Active != "beta" {
		t.Fatalf("active = %q, want beta", view.Active)
	}
}

// The form cannot show the key, so saving without retyping it must not erase it —
// and it must be carried over by name, so one provider's key is not handed to
// another.
func TestSavingWithoutAKeyKeepsTheStoredOneByName(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active: "alpha",
		Providers: map[string]provider.Endpoint{
			"alpha": {APIKey: "sk-alpha", Model: "m"},
			"beta":  {APIKey: "sk-beta", Model: "b"},
		},
	}}
	body := map[string]any{
		"active": "beta",
		"providers": []map[string]any{
			{"name": "alpha", "base_url": "https://alpha.example.test/v1", "model": "m"},
			{"name": "beta", "base_url": "https://beta.example.test/v1", "model": "b"},
		},
	}
	w := putProvider(t, handlerWithProvider(t, stored, Info{}), body)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(stored.written) != 1 {
		t.Fatalf("wrote %d times", len(stored.written))
	}
	written := stored.written[0]
	if written.Providers["alpha"].APIKey != "sk-alpha" || written.Providers["beta"].APIKey != "sk-beta" {
		t.Fatalf("keys were not carried over by name: %+v", written.Providers)
	}
	if written.Active != "beta" {
		t.Fatalf("active = %q, want beta", written.Active)
	}
}

func TestSavingAKeyReplacesTheStoredOne(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active:    "alpha",
		Providers: map[string]provider.Endpoint{"alpha": {APIKey: "sk-old", Model: "m"}},
	}}
	body := oneProvider("alpha", "https://alpha.example.test/v1", "sk-new", "m")
	if w := putProvider(t, handlerWithProvider(t, stored, Info{}), body); w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if got := stored.written[0].Providers["alpha"].APIKey; got != "sk-new" {
		t.Fatalf("key = %q, want the submitted one", got)
	}
}

// Removing a key is deliberate: it is the only way to express "this provider
// should not have one", and it must not happen by leaving a field empty.
func TestClearingTheKeyIsExplicit(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active:    "alpha",
		Providers: map[string]provider.Endpoint{"alpha": {APIKey: "sk-old", Model: "m"}},
	}}
	body := map[string]any{
		"active": "alpha",
		"providers": []map[string]any{
			{"name": "alpha", "base_url": "https://alpha.example.test/v1", "model": "m", "clear_api_key": true},
		},
	}
	if w := putProvider(t, handlerWithProvider(t, stored, Info{}), body); w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if got := stored.written[0].Providers["alpha"].APIKey; got != "" {
		t.Fatalf("key = %q, want it cleared", got)
	}
}

// A provider the form no longer names is gone, and its key goes with it: the page
// owns the list.
func TestAProviderLeftOutOfTheFormIsRemoved(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active: "alpha",
		Providers: map[string]provider.Endpoint{
			"alpha": {APIKey: "sk-alpha", Model: "m"},
			"gone":  {APIKey: "sk-gone", Model: "g"},
		},
	}}
	if w := putProvider(t, handlerWithProvider(t, stored, Info{}), oneProvider("alpha", "https://alpha.example.test/v1", "", "m")); w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	written := stored.written[0]
	if len(written.Providers) != 1 {
		t.Fatalf("providers = %+v, want only the one the form listed", written.Providers)
	}
	if _, ok := written.Providers["gone"]; ok {
		t.Fatalf("a provider the form dropped survived: %+v", written.Providers)
	}
}

// Saving providers a file a run calls through, so a foreign page must not be able
// to do it.
func TestSavingAProviderNeedsTheBoundOrigin(t *testing.T) {
	stored := &fakeProviderFile{}
	body, err := json.Marshal(oneProvider("alpha", "https://alpha.example.test/v1", "k", "m"))
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, handlerWithProvider(t, stored, Info{}), http.MethodPut, "/api/provider", string(body), false)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403 without the bound Origin", w.Code)
	}
	if len(stored.written) != 0 {
		t.Fatal("a request without the bound Origin wrote the file")
	}
}

// A file that cannot be used is refused with the reason, and nothing reaches the
// disk. The reason names the offending value, because the reader is looking at a
// form full of names.
func TestAProviderThatCannotBeUsedIsRefused(t *testing.T) {
	stored := &fakeProviderFile{}
	body := map[string]any{
		"active":    "nobody",
		"providers": []map[string]any{{"name": "alpha", "base_url": "https://alpha.example.test/v1", "model": "m"}},
	}
	w := putProvider(t, handlerWithProvider(t, stored, Info{}), body)
	if w.Code != 400 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "nobody") {
		t.Fatalf("the refusal does not name what is wrong: %s", w.Body)
	}
	if len(stored.written) != 0 {
		t.Fatalf("a refused save wrote %+v", stored.written)
	}
}

// A body that says two different things about one name is refused rather than
// silently collapsed: the file is a map, and which of the two was saved would
// otherwise never be said.
func TestADuplicateNameInTheFormIsRefused(t *testing.T) {
	stored := &fakeProviderFile{}
	body := map[string]any{
		"active": "alpha",
		"providers": []map[string]any{
			{"name": "alpha", "base_url": "https://one.example.test/v1", "model": "m"},
			{"name": "alpha", "base_url": "https://two.example.test/v1", "model": "m"},
		},
	}
	w := putProvider(t, handlerWithProvider(t, stored, Info{}), body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "twice") {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(stored.written) != 0 {
		t.Fatal("a refused save wrote the file")
	}
}

// A Luna that has never been configured is the case this whole surface exists
// for, so the view has to say what is missing rather than reporting an error.
func TestAnEmptyProviderReportsWhatIsMissing(t *testing.T) {
	w := request(t, handlerWithProvider(t, &fakeProviderFile{}, Info{}), http.MethodGet, "/api/provider", "", false)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	view := decodeProvider(t, w.Body.Bytes())
	if view.Active != "" || len(view.Providers) != 0 {
		t.Fatalf("view = %+v, want an empty provider", view)
	}
	if view.Configured {
		t.Fatal("a Luna with no provider is reported as configured")
	}
	if strings.Join(view.Missing, ",") != "provider" {
		t.Fatalf("missing = %v, want the provider itself", view.Missing)
	}
	if view.File != "provider.yaml" {
		t.Fatalf("file = %q", view.File)
	}
}

// A provider that is listed but incomplete says which of its fields is unset, and
// names them in the provider file's own words so the form can point at them.
func TestAnIncompleteProviderNamesItsOwnFields(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active:    "alpha",
		Providers: map[string]provider.Endpoint{"alpha": {BaseURL: "https://alpha.example.test/v1"}},
	}}
	view := decodeProvider(t, request(t, handlerWithProvider(t, stored, Info{}), http.MethodGet, "/api/provider", "", false).Body.Bytes())
	if strings.Join(view.Missing, ",") != "api_key,model" {
		t.Fatalf("missing = %v", view.Missing)
	}
	if view.Configured {
		t.Fatal("an incomplete provider is reported as configured")
	}
}

// Nothing this surface says may tell a person to restart: the file it describes is
// the file the next run reads. The first version of this surface shipped a
// restart_needed field and a Luna that could not be configured into a working one
// without a restart, which is why this is asserted on the whole payload.
func TestNoProviderPayloadTellsAnyoneToRestart(t *testing.T) {
	stored := &fakeProviderFile{stored: provider.File{
		Active:    "alpha",
		Providers: map[string]provider.Endpoint{"alpha": {BaseURL: "https://alpha.example.test/v1", APIKey: "k", Model: "m"}},
	}}
	h := handlerWithProvider(t, stored, Info{})
	for _, w := range []*httptest.ResponseRecorder{
		request(t, h, http.MethodGet, "/api/provider", "", false),
		putProvider(t, h, oneProvider("alpha", "https://alpha.example.test/v1", "", "m")),
	} {
		body := w.Body.String()
		if strings.Contains(body, "restart") {
			t.Fatalf("a provider payload mentions a restart: %s", body)
		}
	}
}

// A storage failure is reported as one: the page must not tell a person their
// provider was saved when it was not.
func TestAStorageFailureIsReported(t *testing.T) {
	stored := &fakeProviderFile{err: errors.New("disk is full")}
	w := putProvider(t, handlerWithProvider(t, stored, Info{}), oneProvider("alpha", "https://alpha.example.test/v1", "k", "m"))
	if w.Code != 500 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
}

// Asking the endpoint which models it serves is the server's call, with the
// server's key: the browser names a provider, it never holds the key.
func TestProbingReportsTheModelsTheEndpointServes(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[{"id":"z"},{"id":"a"}]}`))
	}))
	defer server.Close()
	stored := &fakeProviderFile{stored: provider.File{
		Active:    "alpha",
		Providers: map[string]provider.Endpoint{"alpha": {BaseURL: server.URL, APIKey: "sk-stored", Model: "m"}},
	}}
	body := `{"name":"alpha","base_url":"` + server.URL + `"}`
	w := request(t, handlerWithProvider(t, stored, Info{}), http.MethodPost, "/api/provider/models", body, true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if gotAuth != "Bearer sk-stored" {
		t.Fatalf("authorization = %q, want the named provider's stored key", gotAuth)
	}
	if !strings.Contains(w.Body.String(), `"a"`) || !strings.Contains(w.Body.String(), `"z"`) {
		t.Fatalf("body = %s", w.Body)
	}
}

// Testing an endpoint a person is halfway through typing must not require
// retyping the key: an empty field means the stored one, for the named provider.
func TestProbingFallsBackToTheNamedProvidersKey(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	stored := &fakeProviderFile{stored: provider.File{
		Active: "alpha",
		Providers: map[string]provider.Endpoint{
			"alpha": {BaseURL: "https://alpha.example.test/v1", APIKey: "sk-alpha", Model: "m"},
			"beta":  {BaseURL: server.URL, APIKey: "sk-beta", Model: "b"},
		},
	}}
	body := `{"name":"beta","base_url":"` + server.URL + `"}`
	if w := request(t, handlerWithProvider(t, stored, Info{}), http.MethodPost, "/api/provider/models", body, true); w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if gotAuth != "Bearer sk-beta" {
		t.Fatalf("authorization = %q, want beta's key rather than the active provider's", gotAuth)
	}
}

// A probe that fails is an answer, not a server error: the page shows the
// provider's words next to the field that caused them.
func TestAFailedProbeIsAnsweredRatherThanFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such key", http.StatusUnauthorized)
	}))
	defer server.Close()
	stored := &fakeProviderFile{stored: provider.File{
		Active:    "alpha",
		Providers: map[string]provider.Endpoint{"alpha": {BaseURL: server.URL, APIKey: "sk-secret", Model: "m"}},
	}}
	w := request(t, handlerWithProvider(t, stored, Info{}), http.MethodPost, "/api/provider/models", "{}", true)
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

// The interface reads the running configuration on every request rather than the
// one this process started with: a provider saved a moment ago is what /api/models,
// /model and /api/state report, with no restart in between. This is the change
// that made "save, then restart Luna" unnecessary.
func TestTheInterfaceFollowsAProviderSavedWhileItRuns(t *testing.T) {
	live := &fakeConfigSource{cfg: config.Config{
		Model: "before", ProviderHost: "before.example.test", BaseURL: "https://before.example.test/v1",
		Models: []config.Model{{Name: "before", Provider: "alpha"}},
	}}
	h := handlerWithProvider(t, &fakeProviderFile{}, Info{Model: "startup", ProviderHost: "startup.example.test"}, WithConfigSource(live))

	before := decodeModels(t, request(t, h, http.MethodGet, "/api/models", "", false).Body.Bytes())
	if before.Models[0].Name != "before" || before.Models[0].Provider != "alpha" {
		t.Fatalf("models before the save = %+v", before.Models)
	}
	if before.Current.Name != "before" {
		t.Fatalf("current before the save = %+v", before.Current)
	}

	// Same server, same process, no restart: the provider file changed.
	live.cfg = config.Config{
		Model: "after", ProviderHost: "after.example.test", BaseURL: "https://after.example.test/v1",
		Models: []config.Model{{Name: "after", Provider: "beta"}, {Name: "after-extra", Provider: "beta"}},
	}
	after := decodeModels(t, request(t, h, http.MethodGet, "/api/models", "", false).Body.Bytes())
	if len(after.Models) != 2 || after.Models[0].Name != "after" || after.Models[1].Provider != "beta" {
		t.Fatalf("models after the save = %+v", after.Models)
	}
	if after.Current.Name != "after" {
		t.Fatalf("current after the save = %+v, want the new default", after.Current)
	}

	commands := decodeCommands(t, request(t, h, http.MethodGet, "/api/commands", "", false))
	var model *commandEntry
	for i := range commands {
		if commands[i].Name == "model" {
			model = &commands[i]
		}
	}
	if model == nil {
		t.Fatal("/model is missing although the provider serves two models")
	}
	values := make([]string, 0, len(model.Options))
	for _, option := range model.Options {
		values = append(values, option.Value)
	}
	if strings.Join(values, ",") != "after,after-extra,--default" {
		t.Fatalf("options = %v, want the models of the provider now in use", values)
	}

	state := decodeState(t, request(t, h, http.MethodGet, "/api/state", "", false).Body.Bytes())
	if state["model"] != "after" || state["provider_host"] != "after.example.test" {
		t.Fatalf("state = %+v, want the provider now in use", state)
	}
}

// A Luna with no provider yet has no models to switch between, so /model does not
// exist: a command that offers no choice is not a command.
func TestNoProviderMeansNoModelCommandAndNoModels(t *testing.T) {
	live := &fakeConfigSource{cfg: config.Config{Missing: []string{"provider"}}}
	h := handlerWithProvider(t, &fakeProviderFile{}, Info{}, WithConfigSource(live))

	models := decodeModels(t, request(t, h, http.MethodGet, "/api/models", "", false).Body.Bytes())
	if len(models.Models) != 0 || models.Current.Name != "" {
		t.Fatalf("models = %+v, want none and no current model", models)
	}
	commands := decodeCommands(t, request(t, h, http.MethodGet, "/api/commands", "", false))
	for _, cmd := range commands {
		if cmd.Name == "model" {
			t.Fatal("/model is offered although there is nothing to switch to")
		}
	}
	state := decodeState(t, request(t, h, http.MethodGet, "/api/state", "", false).Body.Bytes())
	missing, _ := state["provider_missing"].([]any)
	if len(missing) != 1 || missing[0] != "provider" {
		t.Fatalf("provider_missing = %v", state["provider_missing"])
	}
}

// A provider file that cannot be read is reported as a problem, not hidden behind
// the values this process started with: those are not what the next run would use.
func TestAProviderThatCannotBeReadIsReported(t *testing.T) {
	live := &fakeConfigSource{err: errors.New(`provider.yaml: active names the provider "nobody", which is not listed`)}
	h := handlerWithProvider(t, &fakeProviderFile{}, Info{Model: "startup", ProviderHost: "startup.example.test"}, WithConfigSource(live))

	w := request(t, h, http.MethodGet, "/api/models", "", false)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "nobody") {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	state := decodeState(t, request(t, h, http.MethodGet, "/api/state", "", false).Body.Bytes())
	if problem, _ := state["provider_problem"].(string); !strings.Contains(problem, "nobody") {
		t.Fatalf("provider_problem = %v, want the reason", state["provider_problem"])
	}
}

// Without a source the server reports what it was built with, which is how an
// embedder that owns the provider itself (and the tests) build one.
func TestAServerWithoutASourceReportsWhatItWasBuiltWith(t *testing.T) {
	h := handlerWithProvider(t, &fakeProviderFile{}, Info{
		Model: "built-in", ProviderHost: "built.example.test",
		Models: []ModelRef{{Name: "built-in", Provider: "built"}},
	})
	models := decodeModels(t, request(t, h, http.MethodGet, "/api/models", "", false).Body.Bytes())
	if len(models.Models) != 1 || models.Models[0].Name != "built-in" || models.Current.Name != "built-in" {
		t.Fatalf("models = %+v", models)
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

func decodeState(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return body
}
