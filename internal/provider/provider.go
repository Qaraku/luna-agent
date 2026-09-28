// Package provider owns provider.yaml: which OpenAI-compatible endpoints Luna
// knows, with which keys and which models, and which of them is the one a run is
// sent to.
//
// This is the file Luna writes. config.yaml is the file the user writes and Luna
// only reads; they stay separate because a program that rewrites a hand-edited
// file destroys the comments around every value it did not change. The browser
// settings page is the ordinary way to fill this file in, and editing it by hand
// works too — one of them is a convenience, not the definition.
//
// Endpoints are named because a run has to be able to say which one it means:
// "the endpoint" is not something a person can keep two of, and a name is what
// the interface shows, what /model reports, and what the log blames when
// something is wrong. One of the entries is active — the one a run that names
// nothing is sent to.
//
// Only an endpoint and an API key are described here. Official subscriptions
// and OAuth-style logins are deliberately out of scope: they need a per-provider
// flow, and a field meaning "whatever that provider's own login is" would make
// this file mean something different depending on which provider read it.
package provider

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/Qaraku/luna-agent/internal/atomicfile"
)

// FileName is the file Luna owns inside its configuration directory. It is a
// name, not a path: where that directory is belongs to internal/layout.
const FileName = "provider.yaml"

// fileMode is the permission of the file. It holds an API key, so it is written
// private and explicitly, rather than inheriting whatever the umask would give.
const fileMode fs.FileMode = 0o600

// Endpoint is one named provider: where it is called, with which key, and which
// models it may be asked for.
//
// Every field is optional on disk, because a half-filled form has to be
// representable: the settings page saves the endpoint before it knows the model,
// and the loader — not this file — is where "which fields are still missing"
// belongs.
type Endpoint struct {
	// BaseURL is an absolute http(s) endpoint of an OpenAI-compatible API.
	BaseURL string `yaml:"base_url"`
	// APIKey is the secret the endpoint is called with. It is never sent back
	// to any interface: what an interface may read is whether one is set and
	// its last four characters.
	APIKey string `yaml:"api_key"`
	// Model is the model this provider is asked for when a run names none.
	Model string `yaml:"model"`
	// Models are the other models this provider may be asked for, in the order
	// the interface lists them. The default one is not repeated here.
	Models []string `yaml:"models"`
}

// File is the provider file: the endpoints Luna knows, and which of them is
// active.
//
// The whole file may be empty, and that is the normal state of a Luna that has
// never been configured — a first run, not a failure. What may not be
// inconsistent is the pair: Active either names a provider that is listed, or
// nothing is listed at all and Active is empty.
type File struct {
	// Active is the provider a run that names none is sent to. It must name one
	// of Providers whenever there is one.
	Active string `yaml:"active"`
	// Providers are the named endpoints, by the name the interface shows.
	Providers map[string]Endpoint `yaml:"providers"`
}

// Trimmed returns the values without surrounding space, treating a value that is
// only whitespace as unset. A key pasted into a form arrives with a trailing
// newline often enough that keeping it would produce an authentication failure
// with no visible cause; a name is typed, so a stray space in it would make it
// unfindable by the name the interface shows.
func (f File) Trimmed() File {
	providers := make(map[string]Endpoint, len(f.Providers))
	for name, entry := range f.Providers {
		models := make([]string, 0, len(entry.Models))
		for _, model := range entry.Models {
			models = append(models, strings.TrimSpace(model))
		}
		if len(models) == 0 {
			models = nil
		}
		providers[strings.TrimSpace(name)] = Endpoint{
			BaseURL: strings.TrimSpace(entry.BaseURL),
			APIKey:  strings.TrimSpace(entry.APIKey),
			Model:   strings.TrimSpace(entry.Model),
			Models:  models,
		}
	}
	if len(providers) == 0 {
		providers = nil
	}
	return File{Active: strings.TrimSpace(f.Active), Providers: providers}
}

// Empty reports whether the file states nothing at all. A provider that states
// nothing is an installation that has not been configured yet, which is a
// starting state rather than an error.
func (f File) Empty() bool {
	t := f.Trimmed()
	return t.Active == "" && len(t.Providers) == 0
}

// Validate refuses a file that could not be read back as itself.
//
// It checks form and consistency, not completeness: a provider whose endpoint is
// not an absolute http(s) URL, a key that carries a line break, or an active
// name that points at nothing is refused with the reason. A missing field is not
// a failure here — the caller that needs it says what it needs.
func (f File) Validate() error {
	t := f.Trimmed()
	for name, entry := range t.Providers {
		if err := validName(name); err != nil {
			return err
		}
		if err := entry.validate(); err != nil {
			return fmt.Errorf("the provider %q: %w", name, err)
		}
	}
	_, _, found := t.Selection()
	switch {
	case len(t.Providers) == 0 && t.Active != "":
		return fmt.Errorf("active names the provider %q, but no provider is listed", t.Active)
	case len(t.Providers) > 0 && t.Active == "":
		return fmt.Errorf("active is empty, so a run would not know which of the %d listed providers to call", len(t.Providers))
	case t.Active != "" && !found:
		return fmt.Errorf("active names the provider %q, which is not listed", t.Active)
	}
	return nil
}

// validName checks one provider name: a name is how an interface, a log line and
// a run all refer to the same endpoint, so it has to be there and it has to be a
// single token.
func validName(name string) error {
	if name == "" {
		return fmt.Errorf("a provider name must not be empty")
	}
	if strings.ContainsFunc(name, unicode.IsSpace) {
		return fmt.Errorf("the provider name %q must not contain whitespace", name)
	}
	return nil
}

// validate checks one endpoint's form. It says what is wrong with which field,
// because the reader is looking at a form.
func (e Endpoint) validate() error {
	if e.BaseURL != "" {
		u, err := url.Parse(e.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("base_url must be an absolute http(s) URL")
		}
	}
	if strings.ContainsAny(e.APIKey, "\r\n") {
		return fmt.Errorf("api_key must be a single line")
	}
	if strings.ContainsAny(e.Model, "\r\n") {
		return fmt.Errorf("model must be a single line")
	}
	seen := make(map[string]bool, len(e.Models))
	for i, model := range e.Models {
		if model == "" {
			return fmt.Errorf("models has an empty entry (number %d)", i+1)
		}
		if model == e.Model {
			return fmt.Errorf("models lists %q, which is already the model", model)
		}
		if seen[model] {
			return fmt.Errorf("models lists %q twice", model)
		}
		seen[model] = true
	}
	return nil
}

// Selection returns the active provider and its name. The third value reports
// whether there is one at all: a file with no active provider is an installation
// that has not been configured yet.
func (f File) Selection() (name string, entry Endpoint, ok bool) {
	name = strings.TrimSpace(f.Active)
	if name == "" {
		return "", Endpoint{}, false
	}
	entry, ok = f.Providers[name]
	return name, entry, ok
}

// Names lists the provider names in sorted order, which is the order an
// interface renders them in. Sorted rather than map order: a form that reshuffles
// itself between two reads is a form nobody can use.
func (f File) Names() []string {
	names := make([]string, 0, len(f.Providers))
	for name := range f.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Hint describes a key without disclosing it: enough for the person who set it
// to recognise which one this is, and nothing that can be used to call anybody.
// An empty key has no hint, because a hint about nothing would read as if a key
// were set.
func Hint(key string) string {
	t := strings.TrimSpace(key)
	if t == "" {
		return ""
	}
	if len(t) <= 4 {
		return strings.Repeat("•", len(t))
	}
	return "••••" + t[len(t)-4:]
}

// LoadFile reads the provider file at path.
//
// A file that is not there is not an error: a Luna that has never been
// configured is a normal Luna, and refusing to start because a file nobody
// created is missing would turn the first run into a failure. The second return
// value reports whether a file was found, so the caller can say which source it
// used instead of guessing.
//
// Unknown keys are refused, for the same reason config.yaml refuses them: a
// misspelled key that silently does nothing leaves the user with no effect and
// no explanation.
func LoadFile(path string) (File, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		// The message names the file, not the directory layout the host
		// happens to have.
		return File{}, false, fmt.Errorf("read %s: %w", filepath.Base(path), reason(err))
	}
	var file File
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty file is an unconfigured provider, not a broken one.
			return File{}, true, nil
		}
		return File{}, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	file = file.Trimmed()
	if err := file.Validate(); err != nil {
		return File{}, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return file, true, nil
}

// Save writes the file, replacing it whole.
//
// Validation runs first, so a file that cannot be read back never reaches the
// disk. The write itself is the shared atomic one: a reader sees the old
// contents or the new ones, never a half-written provider.
func Save(path string, file File) error {
	file = file.Trimmed()
	if err := file.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(file)
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	return atomicfile.WriteFile(path, data, fileMode)
}

// reason reduces a path error to its underlying reason, so an error about a
// configuration file does not echo the host's directory layout.
func reason(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
