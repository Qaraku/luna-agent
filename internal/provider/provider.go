// Package provider owns provider.yaml: which OpenAI-compatible endpoint Luna
// calls, with which key, and for which model.
//
// This is the file Luna writes. config.yaml is the file the user writes and Luna
// only reads; they stay separate because a program that rewrites a hand-edited
// file destroys the comments around every value it did not change. The browser
// settings page is the ordinary way to fill this file in, and editing it by hand
// works too — one of them is a convenience, not the definition.
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
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Qaraku/luna-agent/internal/atomicfile"
)

// FileName is the file Luna owns inside its configuration directory. It is a
// name, not a path: where that directory is belongs to internal/layout.
const FileName = "provider.yaml"

// fileMode is the permission of the file. It holds an API key, so it is written
// private and explicitly, rather than inheriting whatever the umask would give.
const fileMode fs.FileMode = 0o600

// File is the provider Luna is configured to call.
//
// Every field is optional on disk, because a half-filled form has to be
// representable: the settings page saves the endpoint before it knows the model,
// and the loader — not this file — is where "which fields are still missing"
// belongs.
type File struct {
	// BaseURL is an absolute http(s) endpoint of an OpenAI-compatible API.
	BaseURL string `yaml:"base_url"`
	// APIKey is the secret the endpoint is called with.
	APIKey string `yaml:"api_key"`
	// Model is the model name the endpoint is asked for.
	Model string `yaml:"model"`
}

// Trimmed returns the values without surrounding space, treating a value that is
// only whitespace as unset. A key pasted into a form arrives with a trailing
// newline often enough that keeping it would produce an authentication failure
// with no visible cause.
func (f File) Trimmed() File {
	return File{
		BaseURL: strings.TrimSpace(f.BaseURL),
		APIKey:  strings.TrimSpace(f.APIKey),
		Model:   strings.TrimSpace(f.Model),
	}
}

// Empty reports whether the file states nothing at all. A provider that states
// nothing is an installation that has not been configured yet, which is a
// starting state rather than an error.
func (f File) Empty() bool {
	t := f.Trimmed()
	return t.BaseURL == "" && t.APIKey == "" && t.Model == ""
}

// Validate refuses a file that could not be read back as itself.
//
// It checks form, not completeness: an endpoint that is not an absolute http(s)
// URL, or a key that carries a line break, is refused with the reason. A missing
// field is not a failure here — the caller that needs it says what it needs.
func (f File) Validate() error {
	t := f.Trimmed()
	if t.BaseURL != "" {
		u, err := url.Parse(t.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("the provider base URL must be an absolute http(s) URL")
		}
	}
	if strings.ContainsAny(t.APIKey, "\r\n") {
		return fmt.Errorf("the API key must be a single line")
	}
	return nil
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
