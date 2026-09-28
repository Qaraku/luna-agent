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
// It holds the choices about how a run is made, and nothing about which provider
// it is sent to: the endpoint, the key and the models belong to provider.yaml,
// which the settings page owns. Keeping them out of here is what makes "the
// settings page is where a provider is set" true rather than a matter of which
// file was read last.
type File struct {
	// ReasoningEffort overrides LUNA_REASONING_EFFORT.
	ReasoningEffort string `yaml:"reasoning_effort"`
	// MaxIterations is how many model turns one run may take before the agent
	// stops it as a runaway loop, overriding LUNA_MAX_ITERATIONS. Zero means
	// not stated, and the layer that owns the budget applies its own default:
	// the number is stated once, not twice.
	MaxIterations int `yaml:"max_iterations"`
	// RunTimeout is how long one run may take before it is stopped and
	// reported as cancelled, overriding LUNA_RUN_TIMEOUT. It is a Go duration
	// such as "20m" or "90s"; empty means not stated.
	RunTimeout string `yaml:"run_timeout"`
}

// Model is one model a run may be sent to, and the provider it belongs to.
//
// It is not an endpoint: which endpoint and key a run uses is decided by the
// active provider in provider.yaml, and the settings page owns that. What a
// model entry carries is the name a run asks for and the name of the provider
// that serves it, which is the label the interface shows.
type Model struct {
	// Name is what the provider is asked for, and the value /model accepts.
	Name string
	// Provider is the name of the provider this model belongs to, as
	// provider.yaml lists it. A run is sent to the active provider, whose name
	// this is.
	Provider string
}

// LoadFile reads the configuration file at path.
//
// A file that is not there is not an error: every setting it can state is also
// readable from the environment, and a runtime that refuses to start because a
// file it may never need is missing would be inventing a problem. The second
// return value reports whether a file was found, so a caller can say which source
// it used instead of guessing.
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

// trimmed returns the file's values without surrounding space, treating a value
// that is only whitespace as unset. A field the user left empty means "no
// opinion" rather than "the empty string", which is what lets the environment
// fill it in.
func (f File) trimmed() File {
	return File{
		ReasoningEffort: strings.TrimSpace(f.ReasoningEffort),
		MaxIterations:   f.MaxIterations,
		RunTimeout:      strings.TrimSpace(f.RunTimeout),
	}
}
