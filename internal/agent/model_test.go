package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/cloudwego/eino/components/model"
)

// namedModel is a fake provider that knows its own name. A test can then say
// which client a run reached, not merely that some client answered — which is
// what "the run was sent to the model it named" has to be proven on.
type namedModel struct {
	name string
	*captureModel
}

func newNamedModel(name, answer string) *namedModel {
	return &namedModel{name: name, captureModel: &captureModel{answer: answer}}
}

// calls is how many times this client was asked anything.
func (m *namedModel) calls() int { return len(m.all()) }

// lastText is the last input the client was given, so the test can check that
// the right question reached the right model.
func (m *namedModel) lastText() string {
	inputs := m.all()
	if len(inputs) == 0 {
		return ""
	}
	var text strings.Builder
	for _, message := range inputs[len(inputs)-1] {
		text.WriteString(message.Content)
	}
	return text.String()
}

// withFakeClients stands fake providers in for the configured ones. No request
// may leave for a provider, and a model the test did not supply has no client.
func withFakeClients(clients ...*namedModel) Option {
	return func(r *Runner) {
		byName := make(map[string]*namedModel, len(clients))
		for _, client := range clients {
			byName[client.name] = client
		}
		r.clientFor = func(_ context.Context, _ config.Config, entry config.Model) (model.ToolCallingChatModel, error) {
			client, ok := byName[entry.Name]
			if !ok {
				return nil, fmt.Errorf("the test has no client for %q", entry.Name)
			}
			return client, nil
		}
	}
}

// modelTestConfig is a configured model list: the first name is the default.
func modelTestConfig(names ...string) config.Config {
	models := make([]config.Model, 0, len(names))
	for _, name := range names {
		models = append(models, config.Model{Name: name, BaseURL: "http://127.0.0.1:1/v1", APIKeyEnv: config.APIKeyEnv})
	}
	return config.Config{BaseURL: models[0].BaseURL, APIKey: "test-key", Model: names[0], ProviderHost: "127.0.0.1", Models: models}
}

// runOnce performs one run and reports what came back.
func runOnce(t *testing.T, r *Runner, runID, modelName, message string) (string, error) {
	t.Helper()
	return r.Run(context.Background(), RunRequest{Message: message, RunID: runID, SessionID: "session-1", Model: modelName, Sink: &collectingSink{}})
}

// A run is sent to the model it names. Successive runs on one runner therefore
// end at different clients, and a run that names the default entry explicitly is
// the same as a run that names nothing.
func TestEachRunIsSentToTheModelItNames(t *testing.T) {
	alpha := newNamedModel("alpha", "answer from alpha")
	beta := newNamedModel("beta", "answer from beta")
	built := map[string]int{}
	runner, err := NewRunner(context.Background(), alpha, fakeInvoker{}, &recordingReader{}, WithConfig(modelTestConfig("alpha", "beta")), withFakeClients(alpha, beta), func(r *Runner) {
		inner := r.clientFor
		r.clientFor = func(ctx context.Context, cfg config.Config, entry config.Model) (model.ToolCallingChatModel, error) {
			built[entry.Name]++
			return inner(ctx, cfg, entry)
		}
	})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}

	// No model named: the configured default.
	answer, err := runOnce(t, runner, "run-1", "", "first")
	if err != nil {
		t.Fatalf("run-1: %v", err)
	}
	if answer != "answer from alpha" || alpha.calls() != 1 || beta.calls() != 0 {
		t.Fatalf("run-1 answer=%q alpha=%d beta=%d", answer, alpha.calls(), beta.calls())
	}
	if !strings.Contains(alpha.lastText(), "first") || strings.Contains(beta.lastText(), "first") {
		t.Fatalf("run-1 reached alpha with %q and beta with %q", alpha.lastText(), beta.lastText())
	}

	// Another model by name: the run is answered by that client.
	answer, err = runOnce(t, runner, "run-2", "beta", "second")
	if err != nil {
		t.Fatalf("run-2: %v", err)
	}
	if answer != "answer from beta" || beta.calls() != 1 || alpha.calls() != 1 {
		t.Fatalf("run-2 answer=%q alpha=%d beta=%d", answer, alpha.calls(), beta.calls())
	}
	if !strings.Contains(beta.lastText(), "second") {
		t.Fatalf("run-2 reached beta with %q", beta.lastText())
	}

	// Naming the default entry is the same client as naming none, and switching
	// back is usable: the client is cached, not rebuilt per run.
	answer, err = runOnce(t, runner, "run-3", "alpha", "third")
	if err != nil {
		t.Fatalf("run-3: %v", err)
	}
	if answer != "answer from alpha" || alpha.calls() != 2 || beta.calls() != 1 {
		t.Fatalf("run-3 answer=%q alpha=%d beta=%d", answer, alpha.calls(), beta.calls())
	}
	answer, err = runOnce(t, runner, "run-4", "beta", "fourth")
	if err != nil {
		t.Fatalf("run-4: %v", err)
	}
	if answer != "answer from beta" || beta.calls() != 2 {
		t.Fatalf("run-4 answer=%q beta=%d", answer, beta.calls())
	}
	if len(built) != 1 || built["beta"] != 1 {
		t.Fatalf("clients built=%v, want beta built exactly once", built)
	}
}

// An unknown model name fails the run. It must not be answered by the default:
// the answer says nothing about which model produced it, so a silent fallback
// would look like a switch that took effect and never did.
func TestAnUnknownModelFailsTheRunInsteadOfFallingBack(t *testing.T) {
	alpha := newNamedModel("alpha", "answer from alpha")
	beta := newNamedModel("beta", "answer from beta")
	runner, err := NewRunner(context.Background(), alpha, fakeInvoker{}, &recordingReader{}, WithConfig(modelTestConfig("alpha", "beta")), withFakeClients(alpha, beta))
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}

	answer, err := runOnce(t, runner, "run-1", "gamma", "hi")
	if err == nil {
		t.Fatalf("a run named the unconfigured model %q and was answered %q", "gamma", answer)
	}
	if answer != "" {
		t.Fatalf("answer=%q, want nothing", answer)
	}
	for _, want := range []string{"gamma", "alpha", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error=%v, want it to mention %q", err, want)
		}
	}
	if alpha.calls() != 0 || beta.calls() != 0 {
		t.Fatalf("the failed run still reached a model: alpha=%d beta=%d", alpha.calls(), beta.calls())
	}

	// The failure is the run's, not the runner's: the next run works.
	answer, err = runOnce(t, runner, "run-2", "beta", "still here")
	if err != nil {
		t.Fatalf("run after a failed one: %v", err)
	}
	if answer != "answer from beta" {
		t.Fatalf("answer=%q", answer)
	}
}

// A runner with no model list at all — the shape the other tests build — still
// refuses a named model rather than answering with the model it was started
// with.
func TestANamedModelNeedsAConfiguredList(t *testing.T) {
	m := newNamedModel("alpha", "answer from alpha")
	runner, err := NewRunner(context.Background(), m, fakeInvoker{}, &recordingReader{})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	if _, err := runOnce(t, runner, "run-1", "beta", "hi"); err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("error=%v, want a refusal naming %q", err, "beta")
	}
	if m.calls() != 0 {
		t.Fatalf("the refused run reached a model: %d calls", m.calls())
	}
	// Naming nothing is still the default, which is what this runner has.
	answer, err := runOnce(t, runner, "run-2", "", "hi")
	if err != nil {
		t.Fatalf("run with no model named: %v", err)
	}
	if answer != "answer from alpha" || m.calls() != 1 {
		t.Fatalf("answer=%q calls=%d", answer, m.calls())
	}
}
