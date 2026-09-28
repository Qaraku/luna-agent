package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/provider"
)

// providerPath is the interface's view of the provider file: the named endpoints
// Luna knows, which of them a run is sent to, and whether each holds a key —
// never a key itself.
const providerPath = "/api/provider"

// providerModelsPath asks one endpoint which models it serves. The call is made
// by the server, because the key belongs to this installation and a browser that
// could ask Luna to spend it would be a browser that could use it.
const providerModelsPath = "/api/provider/models"

// probeTimeout bounds the outbound call. A provider that does not answer must
// not hold a settings page open, and a person waiting on a form needs an answer
// either way.
const probeTimeout = 15 * time.Second

// ProviderConfig is the provider file as this layer uses it. Where the file
// lives, and how it is written, belong to whoever supplied this — the settings
// page owns the request shape and the status codes, not the storage.
type ProviderConfig interface {
	// Read returns the provider file as it is stored. A file that is not there
	// yet is an empty file, not an error: every Luna starts that way.
	Read() (provider.File, error)
	// Write replaces the stored file. An error means nothing was written.
	Write(provider.File) error
}

// WithProvider supplies the provider file. A server built without one answers
// that no provider can be edited, rather than failing to start.
func WithProvider(config ProviderConfig) Option { return func(s *Server) { s.provider = config } }

// ConfigSource is the running configuration as the interface reads it: the
// provider a run started right now would be sent to.
//
// It exists because saving a provider takes effect on the next run rather than at
// the next start. A server that reported only what it was built with would keep
// telling a person to restart after the runner had already re-read the file —
// which is precisely the answer this surface exists to stop giving. A server
// built without a source reports what it was built with; that is the shape a test
// and any embedder that owns the provider itself uses.
type ConfigSource interface {
	Current() (config.Config, error)
}

// WithConfigSource supplies the source the server asks what a run started now
// would use.
func WithConfigSource(src ConfigSource) Option { return func(s *Server) { s.configSource = src } }

// providerFile is the name a person is told to look at. It is a name, not a
// path: the host's directory layout is not the interface's business.
const providerFileName = provider.FileName

// providerRef is one named provider as the browser sees it. KeySet and KeyHint
// are the only things about a key that ever leave the process: enough for the
// person who stored it to recognise which one it is, and nothing that can be used
// to call anybody.
type providerRef struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	Model   string   `json:"model"`
	Models  []string `json:"models"`
	KeySet  bool     `json:"key_set"`
	KeyHint string   `json:"key_hint"`
}

// providerView is the frozen shape of GET /api/provider.
//
// There is no field saying a restart is needed, and that is deliberate: the file
// this describes is the file the next run reads, so what is stored and what is in
// use are the same thing.
type providerView struct {
	// Active names the provider a run is sent to, and is empty for a Luna that
	// has never been configured.
	Active string `json:"active"`
	// Providers are the named endpoints, sorted by name so a form does not
	// reshuffle itself between two reads.
	Providers []providerRef `json:"providers"`
	// Configured says whether a run started now would work: the active provider
	// has an endpoint, a key and a model.
	Configured bool `json:"configured"`
	// Missing names what saving would still leave unset, in the provider file's
	// own field names, so the form can point at them instead of leaving a person
	// to guess.
	Missing []string `json:"missing"`
	// File is the file the settings page writes, named so a person can look at
	// it (or back it up) without being told a directory.
	File string `json:"file"`
}

// sendProvider answers with the stored provider file, as the interface sees it.
func (s *Server) sendProvider(w http.ResponseWriter, file provider.File) {
	send(w, 200, providerViewOf(file))
}

// providerViewOf renders the stored file. Nothing here reads the running
// configuration: the view describes the file, and the file is what the next run
// reads.
func providerViewOf(file provider.File) providerView {
	file = file.Trimmed()
	view := providerView{Active: file.Active, Providers: make([]providerRef, 0, len(file.Providers)), File: providerFileName}
	for _, name := range file.Names() {
		entry := file.Providers[name]
		models := entry.Models
		if models == nil {
			models = []string{}
		}
		view.Providers = append(view.Providers, providerRef{
			Name:    name,
			BaseURL: entry.BaseURL,
			Model:   entry.Model,
			Models:  models,
			KeySet:  entry.APIKey != "",
			KeyHint: provider.Hint(entry.APIKey),
		})
	}
	view.Missing = missingProviderFields(file)
	view.Configured = len(view.Missing) == 0
	return view
}

// missingProviderFields names what a run started now would still lack. It is
// computed from the active provider, because that is the one a run would call:
// reporting an unset endpoint for a provider nobody calls would point at the
// wrong field.
func missingProviderFields(file provider.File) []string {
	_, entry, ok := file.Selection()
	if !ok {
		return []string{"provider"}
	}
	missing := make([]string, 0, 3)
	if entry.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if entry.APIKey == "" {
		missing = append(missing, "api_key")
	}
	if entry.Model == "" {
		missing = append(missing, "model")
	}
	return missing
}

// getProvider answers with the stored provider file.
func (s *Server) getProvider(w http.ResponseWriter) {
	file, err := s.readProvider()
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.sendProvider(w, file)
}

// readProvider reads the stored file, treating a server without one as an empty
// file so the page still renders: a Luna that cannot be configured is worse than
// one that reads back empty.
func (s *Server) readProvider() (provider.File, error) {
	if s.provider == nil {
		return provider.File{}, nil
	}
	return s.provider.Read()
}

// providerInput is the form's submission: the whole list, plus which of them a
// run should be sent to. The page owns the list, so this is a replacement rather
// than a patch — a provider the form no longer names is gone.
type providerInput struct {
	Active    string            `json:"active"`
	Providers []providerEntryIn `json:"providers"`
}

// providerEntryIn is one provider as the form submits it.
//
// An empty APIKey means "keep the stored one", and that is not a convenience: the
// browser is never given a key, so a form that had to send it back would either
// have to fetch a secret it is not allowed to see or drop it on every save.
// ClearAPIKey is how a person removes one deliberately, because leaving a field
// empty cannot mean two things.
type providerEntryIn struct {
	Name        string   `json:"name"`
	BaseURL     string   `json:"base_url"`
	Model       string   `json:"model"`
	Models      []string `json:"models"`
	APIKey      string   `json:"api_key"`
	ClearAPIKey bool     `json:"clear_api_key"`
}

// setProvider stores the file the settings page submitted.
func (s *Server) setProvider(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		fail(w, 500, fmt.Errorf("this Luna has no provider file to write"))
		return
	}
	var in providerInput
	if !decode(w, r, &in) {
		return
	}
	stored, err := s.provider.Read()
	if err != nil {
		fail(w, 500, err)
		return
	}
	next, err := mergeProvider(stored, in)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.provider.Write(next); err != nil {
		fail(w, 400, err)
		return
	}
	s.addEvent("provider_saved", describeProviderSave(next))
	s.sendProvider(w, next)
}

// mergeProvider turns the form's submission into the file to write.
//
// The keys are the one thing the form cannot state, so they are carried over by
// name: a provider the form keeps keeps its key, and a provider the form drops
// takes its key with it. A name that appears twice is refused rather than
// silently collapsed — the file is a map, and a form that says two different
// things about one name would be saved as one of them without saying which.
func mergeProvider(stored provider.File, in providerInput) (provider.File, error) {
	stored = stored.Trimmed()
	next := provider.File{Active: strings.TrimSpace(in.Active), Providers: make(map[string]provider.Endpoint, len(in.Providers))}
	for _, entry := range in.Providers {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return provider.File{}, fmt.Errorf("a provider in the form has no name")
		}
		if _, twice := next.Providers[name]; twice {
			return provider.File{}, fmt.Errorf("the provider %q appears twice in the form", name)
		}
		key := ""
		if old, ok := stored.Providers[name]; ok {
			key = old.APIKey
		}
		switch {
		case entry.ClearAPIKey:
			key = ""
		case strings.TrimSpace(entry.APIKey) != "":
			key = entry.APIKey
		}
		next.Providers[name] = provider.Endpoint{
			BaseURL: entry.BaseURL,
			APIKey:  key,
			Model:   entry.Model,
			Models:  entry.Models,
		}
	}
	if len(next.Providers) == 0 {
		next.Providers = nil
	}
	return next.Trimmed(), nil
}

// describeProviderSave is the lifecycle line for a provider change. It names the
// provider, the endpoint and the model, never the key: a lifecycle log is read by
// whoever runs the process, and a secret in it would outlive the file it came
// from.
func describeProviderSave(file provider.File) string {
	name, entry, ok := file.Selection()
	if !ok {
		return "provider saved: no provider is in use"
	}
	key := "no key"
	if entry.APIKey != "" {
		key = "a key"
	}
	return fmt.Sprintf("provider saved: %s calls %s for %s, %s", name, entry.BaseURL, entry.Model, key)
}

// probeProvider asks one endpoint which models it serves.
//
// The endpoint and the key may be sent with the request, so a person can test
// what they typed before saving it. Whatever is not sent comes from the provider
// the request names, or from the active one when it names none — a form testing
// an endpoint it is halfway through typing has not retyped the key, and falling
// back to the stored one is what makes "test" work at all. The answer is the
// provider's own words on failure: a probe exists to tell a person what went
// wrong with what they typed.
func (s *Server) probeProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
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
	fallback := provider.Endpoint{}
	if name := strings.TrimSpace(in.Name); name != "" {
		fallback = stored.Trimmed().Providers[name]
	} else if _, entry, ok := stored.Selection(); ok {
		fallback = entry
	}
	base, key := in.BaseURL, in.APIKey
	if base == "" {
		base = fallback.BaseURL
	}
	if key == "" {
		key = fallback.APIKey
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
