package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// FileName is the file Luna reads inside the user's configuration directory.
// It is a name, not a path: where the directory is belongs to internal/layout.
const FileName = "config.yaml"

// File is the user-level configuration file: the settings a person edits by
// hand, as opposed to the environment variables a launcher usually sets.
//
// Luna only reads this file. Writing it would mean rewriting a file the user
// owns — comments included — for a change that is not theirs, so anything Luna
// has to change itself will live somewhere else, in a file it owns.
//
// The key is never a value in this file: APIKeyEnv names the environment
// variable that holds it. A file that says which variable to read keeps working
// when the key rotates, and one that contained the key itself would put a
// secret in the user's dotfiles where backups and screenshots can reach it.
type File struct {
	// Model is the model to ask the provider for. It overrides the model
	// environment variables.
	Model string `yaml:"model"`
	// BaseURL is the provider endpoint. It overrides OPENAI_BASE_URL.
	BaseURL string `yaml:"base_url"`
	// APIKeyEnv names the environment variable holding the key. Empty means
	// OPENAI_API_KEY.
	APIKeyEnv string `yaml:"api_key_env"`
	// ReasoningEffort overrides LUNA_REASONING_EFFORT.
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// LoadFile reads the configuration file at path.
//
// A file that is not there is not an error: most people start with the
// environment alone, and a runtime that refuses to start because a file it may
// never need is missing would be inventing a problem. The second return value
// reports whether a file was found, so a caller can say which source it used
// instead of guessing.
//
// Unknown keys are refused. A configuration file that silently ignores a
// misspelled key is worse than one that fails to load: the user sees no effect
// and no reason.
func LoadFile(path string) (File, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		// The message names the file, not the absolute path it sits in: a
		// configuration error is read by a person who knows where their own
		// files are, and a host layout does not belong in it.
		return File{}, false, fmt.Errorf("read %s: %w", filepath.Base(path), reason(err))
	}
	var file File
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty file is an empty configuration, not a broken one.
			return File{}, true, nil
		}
		return File{}, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return file, true, nil
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

// trim returns the file's values without surrounding space, treating a value
// that is only whitespace as unset. A field the user left empty means "no
// opinion" rather than "the empty string", which is what lets the environment
// fill it in.
func (f File) trimmed() File {
	return File{
		Model:           strings.TrimSpace(f.Model),
		BaseURL:         strings.TrimSpace(f.BaseURL),
		APIKeyEnv:       strings.TrimSpace(f.APIKeyEnv),
		ReasoningEffort: strings.TrimSpace(f.ReasoningEffort),
	}
}
