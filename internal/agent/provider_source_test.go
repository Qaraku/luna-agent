package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/cloudwego/eino/components/model"
)

// fakeSource is a provider source whose answer can be replaced, which is what
// "the settings page saved something" looks like to a runner.
type fakeSource struct {
	mu  sync.Mutex
	cfg config.Config
	err error
}

func (s *fakeSource) Current() (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.err
}

func (s *fakeSource) set(cfg config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// unconfiguredConfig is what the loader reports for a Luna nobody has configured
// yet: no provider at all, and the one field to fill in.
func unconfiguredConfig() config.Config {
	return config.Config{Missing: []string{"provider"}}
}

// providerConfig is one configured provider: an endpoint, a key and a model list
// with the default first, exactly as the loader resolves it.
func providerConfig(name, baseURL, apiKey, defaultModel string, models ...string) config.Config {
	list := make([]config.Model, 0, len(models)+1)
	list = append(list, config.Model{Name: defaultModel, Provider: name})
	for _, extra := range models {
		list = append(list, config.Model{Name: extra, Provider: name})
	}
	host := strings.TrimPrefix(baseURL, "https://")
	host, _, _ = strings.Cut(host, "/")
	return config.Config{BaseURL: baseURL, APIKey: apiKey, Model: defaultModel, ProviderHost: host, Models: list}
}

// builtClient is one client a factory was asked to build: which endpoint it would
// have called, with which key, for which model.
type builtClient struct {
	baseURL string
	apiKey  string
	model   string
}

// recordingClients answers with a fake provider per model name and records every
// client the runner asked for. Nothing here talks to a provider: what is under
// test is which endpoint a run would have been sent to.
type recordingClients struct {
	mu     sync.Mutex
	built  []builtClient
	byName map[string]*namedModel
}

func newRecordingClients(clients ...*namedModel) *recordingClients {
	byName := make(map[string]*namedModel, len(clients))
	for _, client := range clients {
		byName[client.name] = client
	}
	return &recordingClients{byName: byName}
}

// option installs the factory on a runner.
func (c *recordingClients) option() Option {
	return func(r *Runner) {
		r.clientFor = func(_ context.Context, cfg config.Config, modelName string) (model.ToolCallingChatModel, error) {
			c.mu.Lock()
			c.built = append(c.built, builtClient{baseURL: cfg.BaseURL, apiKey: cfg.APIKey, model: modelName})
			c.mu.Unlock()
			client, ok := c.byName[modelName]
			if !ok {
				return nil, fmt.Errorf("the test has no client for %q", modelName)
			}
			return client, nil
		}
	}
}

func (c *recordingClients) last() builtClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.built) == 0 {
		return builtClient{}
	}
	return c.built[len(c.built)-1]
}

func (c *recordingClients) all() []builtClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]builtClient{}, c.built...)
}

// A provider saved while the process runs is the one the next run is sent to,
// with the new endpoint and the new key, and a client built for the old endpoint
// is never reused: the point of saving is that the old one stopped being the
// answer.
func TestASavedProviderIsUsedByTheNextRunWithoutARestart(t *testing.T) {
	first := newNamedModel("first-model", "answer from the first provider")
	second := newNamedModel("second-model", "answer from the second provider")
	clients := newRecordingClients(first, second)
	src := &fakeSource{cfg: providerConfig("alpha", "https://first.example.test/v1", "sk-first", "first-model")}

	runner, err := NewProviderRunner(context.Background(), src, fakeInvoker{}, &recordingReader{}, clients.option())
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}

	answer, err := runOnce(t, runner, "run-1", "", "first question")
	if err != nil {
		t.Fatalf("run-1: %v", err)
	}
	if answer != "answer from the first provider" {
		t.Fatalf("run-1 answer=%q", answer)
	}
	if got := clients.last(); got != (builtClient{"https://first.example.test/v1", "sk-first", "first-model"}) {
		t.Fatalf("run-1 built %+v, want the first provider's endpoint, key and model", got)
	}

	// The settings page saved a different provider.
	src.set(providerConfig("beta", "https://second.example.test/v1", "sk-second", "second-model"))

	answer, err = runOnce(t, runner, "run-2", "", "second question")
	if err != nil {
		t.Fatalf("run-2 after the provider changed: %v", err)
	}
	if answer != "answer from the second provider" {
		t.Fatalf("run-2 answer=%q, want the newly saved provider to answer", answer)
	}
	if got := clients.last(); got != (builtClient{"https://second.example.test/v1", "sk-second", "second-model"}) {
		t.Fatalf("run-2 built %+v, want the newly saved provider", got)
	}
	if first.calls() != 1 {
		t.Fatalf("the first provider was called %d times, want only the run before the save", first.calls())
	}
}

// Saving an endpoint for the same provider changes where a model is reached, so a
// client cached for the old endpoint must not answer for the new one.
func TestAChangedEndpointInvalidatesCachedClients(t *testing.T) {
	fast := newNamedModel("first-model", "answer")
	extra := newNamedModel("extra-model", "answer from extra")
	clients := newRecordingClients(fast, extra)
	src := &fakeSource{cfg: providerConfig("alpha", "https://one.example.test/v1", "sk-one", "first-model", "extra-model")}

	runner, err := NewProviderRunner(context.Background(), src, fakeInvoker{}, &recordingReader{}, clients.option())
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	// The extra model, so that a client lands in the cache rather than in the
	// slot the default model always occupies.
	if _, err := runOnce(t, runner, "run-1", "extra-model", "question"); err != nil {
		t.Fatalf("run-1: %v", err)
	}
	if got := clients.last(); got.baseURL != "https://one.example.test/v1" || got.model != "extra-model" {
		t.Fatalf("run-1 built %+v", got)
	}

	src.set(providerConfig("alpha", "https://two.example.test/v1", "sk-two", "first-model", "extra-model"))
	if _, err := runOnce(t, runner, "run-2", "extra-model", "question"); err != nil {
		t.Fatalf("run-2: %v", err)
	}
	if got := clients.last(); got != (builtClient{"https://two.example.test/v1", "sk-two", "extra-model"}) {
		t.Fatalf("run-2 built %+v, want a client for the new endpoint: a cached client would have called the old one", got)
	}
	rebuilt := 0
	for _, got := range clients.all() {
		if got.model == "extra-model" {
			rebuilt++
		}
	}
	if rebuilt != 2 {
		t.Fatalf("the extra model was built %d times, want once per endpoint (%+v)", rebuilt, clients.all())
	}
	if extra.calls() != 2 {
		t.Fatalf("the extra model was called %d times, want both runs", extra.calls())
	}
}

// A Luna with no provider says so in one actionable sentence, and the same
// process — the same runner — starts working as soon as the settings page has
// written one. That is the whole point of not refusing to build a runner without
// a provider: the page that fixes the installation is served by this process.
func TestTheSameRunnerWorksOnceAProviderIsSaved(t *testing.T) {
	saved := newNamedModel("first-model", "answer from the saved provider")
	clients := newRecordingClients(saved)
	src := &fakeSource{cfg: unconfiguredConfig()}

	runner, err := NewProviderRunner(context.Background(), src, fakeInvoker{}, &recordingReader{}, clients.option())
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}

	answer, err := runOnce(t, runner, "run-1", "", "hello")
	want := "this Luna has no provider configured yet: provider is unset. Fill it in on the settings page, which writes provider.yaml"
	if err == nil || err.Error() != want {
		t.Fatalf("run without a provider: err=%v answer=%q, want exactly %q", err, answer, want)
	}
	if len(clients.all()) != 0 {
		t.Fatalf("a run with no provider built a client: %+v", clients.all())
	}

	// The settings page saves one. Nothing is restarted: the same runner runs.
	src.set(providerConfig("alpha", "https://saved.example.test/v1", "sk-saved", "first-model"))
	answer, err = runOnce(t, runner, "run-2", "", "hello")
	if err != nil {
		t.Fatalf("run after the provider was saved: %v", err)
	}
	if answer != "answer from the saved provider" {
		t.Fatalf("run-2 answer=%q", answer)
	}
	if got := clients.last(); got != (builtClient{"https://saved.example.test/v1", "sk-saved", "first-model"}) {
		t.Fatalf("run-2 built %+v, want the saved provider", got)
	}
}

// A provider that exists but is incomplete names the fields the settings page has
// to fill in, rather than reporting a request that failed somewhere else.
func TestAnIncompleteProviderNamesTheFields(t *testing.T) {
	src := &fakeSource{cfg: config.Config{Model: "m", Missing: []string{"base_url", "api_key"}}}
	runner, err := NewProviderRunner(context.Background(), src, fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	_, err = runOnce(t, runner, "run-1", "", "hello")
	if err == nil {
		t.Fatal("a run with an incomplete provider was accepted")
	}
	for _, want := range []string{"base_url, api_key", "provider.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}

// A source that cannot answer fails the run with its reason: it is not silently
// treated as "no provider", which would tell the user to fill in a form that may
// already be filled in.
func TestAnUnreadableProviderFailsTheRunWithItsReason(t *testing.T) {
	src := &fakeSource{err: errors.New("read provider.yaml: permission denied")}
	runner, err := NewProviderRunner(context.Background(), src, fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	_, err = runOnce(t, runner, "run-1", "", "hello")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err=%v, want the source's reason", err)
	}
	if strings.Contains(err.Error(), "no provider configured") {
		t.Fatalf("err=%v, want it told apart from an unconfigured provider", err)
	}
}

// A runner built around a source has no agent until its first run, and a runner
// built without one keeps the configuration it was given: the two shapes are not
// alternatives to each other, and the second one is what every other test here
// uses.
func TestNewProviderRunnerNeedsASource(t *testing.T) {
	if _, err := NewProviderRunner(context.Background(), nil, fakeInvoker{}, &recordingReader{}); err == nil {
		t.Fatal("a runner was built without a source")
	}
}
