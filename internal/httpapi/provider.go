package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Qaraku/luna-agent/internal/provider"
)

// providerPath is the interface's view of the provider file: the endpoint, the
// model and whether a key is set — never the key itself.
const providerPath = "/api/provider"

// providerModelsPath asks the configured endpoint which models it serves. The
// call is made by the server, because the key belongs to this installation and a
// browser that could ask Luna to spend it would be a browser that could use it.
const providerModelsPath = "/api/provider/models"

// probeTimeout bounds the outbound call. A provider that does not answer must
// not hold a settings page open, and a person waiting on a form needs an answer
// either way.
const probeTimeout = 15 * time.Second

// ProviderConfig is the provider file as this layer uses it. Where the file
// lives, and how it is written, belong to whoever supplied this — the settings
// page owns the request shape and the status codes, not the storage.
type ProviderConfig interface {
	// Read returns the provider as it is stored. A file that is not there yet
	// is an empty provider, not an error: every Luna starts that way.
	Read() (provider.File, error)
	// Write replaces the stored provider. An error means nothing was written.
	Write(provider.File) error
}

// WithProvider supplies the provider file. A server built without one answers
// that no provider can be edited, rather than failing to start.
func WithProvider(config ProviderConfig) Option { return func(s *Server) { s.provider = config } }

// providerFile is the name a person is told to look at. It is a name, not a
// path: the host's directory layout is not the interface's business.
const providerFileName = provider.FileName

type providerView struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// KeySet says whether a key is stored, and KeyHint is the only part of it
	// that ever leaves the process: enough to recognise which key it is, and
	// nothing that can be used to call a provider.
	KeySet  bool   `json:"key_set"`
	KeyHint string `json:"key_hint"`
	// Configured is what the running process is using right now. It is false
	// while any of the three settings is unset, and a run in that state fails
	// with a sentence naming them.
	Configured bool `json:"configured"`
	// Missing names the settings that are still unset, so the form can point at
	// them instead of leaving a person to guess.
	Missing []string `json:"missing"`
	// File is the file the settings page writes, named so a person can look at
	// it (or back it up) without being told a directory.
	File string `json:"file"`
	// RestartNeeded says that what is stored is not what this process is using.
	// The model is built once, when the process starts, so a provider saved now
	// is in effect from the next start — and saying so is the difference between
	// a person restarting and a person believing it did not work.
	RestartNeeded bool `json:"restart_needed"`
}

// sendProvider reports the stored provider and what the running process is
// using. The key is reported as a boolean and a hint, never as a value: the
// browser has no way to read it back, which is what keeps it out of screenshots,
// page dumps and anything that caches a response.
func (s *Server) sendProvider(w http.ResponseWriter, file provider.File) {
	view := providerView{
		BaseURL:    file.BaseURL,
		Model:      file.Model,
		KeySet:     file.APIKey != "",
		KeyHint:    provider.Hint(file.APIKey),
		Configured: true,
		File:       providerFileName,
	}
	if len(s.info.Missing) > 0 || s.info.Model == "" {
		view.Configured = false
	}
	view.Missing = missingProviderFields(file)
	view.RestartNeeded = view.BaseURL != s.info.BaseURL || view.Model != s.info.Model
	send(w, 200, view)
}

// missingProviderFields names what a run would still lack. It is computed from
// the file rather than from the running configuration, because the form is
// editing the file: what it has to say is what saving it would leave unset.
func missingProviderFields(file provider.File) []string {
	missing := make([]string, 0, 3)
	if file.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if file.APIKey == "" {
		missing = append(missing, "api_key")
	}
	if file.Model == "" {
		missing = append(missing, "model")
	}
	return missing
}

// getProvider answers with the stored provider.
func (s *Server) getProvider(w http.ResponseWriter) {
	file, err := s.readProvider()
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.sendProvider(w, file)
}

// readProvider reads the stored provider, treating a server without one as an
// empty file so the page still renders: a Luna that cannot be configured is
// worse than one that reads back empty.
func (s *Server) readProvider() (provider.File, error) {
	if s.provider == nil {
		return provider.File{}, nil
	}
	return s.provider.Read()
}

// setProvider stores the provider the settings page submitted.
//
// An empty API key means "leave the stored one alone", and that is not a
// convenience: the browser is never given the key, so a form that had to send it
// back would either have to fetch a secret it is not allowed to see or drop it
// on every save.
func (s *Server) setProvider(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		fail(w, 500, fmt.Errorf("this Luna has no provider file to write"))
		return
	}
	var in struct {
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
		ClearAPIKey bool   `json:"clear_api_key"`
		Model       string `json:"model"`
	}
	if !decode(w, r, &in) {
		return
	}
	stored, err := s.provider.Read()
	if err != nil {
		fail(w, 500, err)
		return
	}
	next := provider.File{BaseURL: in.BaseURL, Model: in.Model, APIKey: stored.APIKey}
	switch {
	case in.ClearAPIKey:
		next.APIKey = ""
	case in.APIKey != "":
		next.APIKey = in.APIKey
	}
	if err := s.provider.Write(next); err != nil {
		fail(w, 400, err)
		return
	}
	s.addEvent("provider_saved", describeProviderSave(next))
	s.sendProvider(w, next.Trimmed())
}

// describeProviderSave is the lifecycle line for a provider change. It names the
// endpoint and the model, never the key: a lifecycle log is read by whoever runs
// the process, and a secret in it would outlive the file it came from.
func describeProviderSave(file provider.File) string {
	key := "no key"
	if file.APIKey != "" {
		key = "a key"
	}
	return fmt.Sprintf("provider saved: endpoint %s, model %s, %s", file.BaseURL, file.Model, key)
}

// probeProvider asks an endpoint which models it serves.
//
// The endpoint and the key may be sent with the request, so a person can test
// what they typed before saving it; whatever is not sent comes from the stored
// provider. The answer is the provider's own words on failure — a probe exists
// to tell a person what went wrong with what they typed.
func (s *Server) probeProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	stored, err := s.readProvider()
	if err != nil {
		fail(w, 500, err)
		return
	}
	base, key := in.BaseURL, in.APIKey
	if base == "" && key == "" {
		base, key = stored.BaseURL, stored.APIKey
	} else {
		if base == "" {
			base = stored.BaseURL
		}
		if key == "" {
			// A form testing an endpoint it is halfway through typing has not
			// retyped the key; falling back to the stored one is what makes
			// "test" work at all.
			key = stored.APIKey
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	models, err := provider.ListModels(ctx, base, key)
	if err != nil {
		// A probe that fails is an answer, not a server error: the page shows
		// the provider's words next to the field that caused them.
		send(w, 200, map[string]any{"models": []string{}, "problem": err.Error()})
		return
	}
	send(w, 200, map[string]any{"models": models, "problem": ""})
}
