// Package layout decides where Luna keeps things on disk.
//
// Luna is a local runtime with one user, and it keeps five kinds of things that
// have genuinely different lifetimes: configuration the user edits, data they
// would miss (the conversations, and anything the agent was asked to keep),
// state that is merely inconvenient to lose (logs, last-session markers),
// caches that must be safe to delete, and process-scoped files (sockets, pid
// files, locks) that must not survive a reboot.
//
// These are resolved through the XDG Base Directory variables rather than a
// single dot-directory, because the difference matters at the moment someone
// clears a cache: with one directory, the same command that frees disk space
// also deletes their conversations. The specification's own test for the
// state/data split is used here — something only meaningful locally and
// cheap to lose is state; something that is the user's own content is data.
//
// Nothing in this package reads or writes a file. It only answers where things
// belong, so that the composition root can decide what to do with the answer.
package layout

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DirName is the directory Luna owns inside each XDG root.
const DirName = "luna"

// Paths is the set of directories Luna uses. Every field is absolute.
type Paths struct {
	// Install holds web/ and plugins/: the assets of the running Luna itself.
	// It is not an XDG root because it is not per-user data — it is wherever
	// this copy of Luna lives.
	Install string
	// Config holds user-edited preferences.
	Config string
	// Data holds what the user would miss: conversations, and anything the agent
	// was asked to keep.
	Data string
	// State holds what should survive a restart but is cheap to lose: logs and
	// "which session was open" style markers.
	State string
	// Cache holds anything that can be rebuilt from scratch. Deleting it must
	// never lose a conversation.
	Cache string
	// Runtime holds process-scoped files — sockets, pid files, locks. It is
	// empty when XDG_RUNTIME_DIR is unset, because the specification defines no
	// default for it; callers that need it fall back to State and say so.
	Runtime string
}

// Env is the environment this package reads, as names to be looked up. Passing
// it in keeps the resolution testable without touching the process
// environment, and keeps the variable names in one place.
type Env func(name string) string

// Resolve returns the directories Luna uses, given the environment and the
// user's home directory.
//
// An XDG variable that is unset, empty or relative is ignored in favour of its
// default: the specification declares relative paths invalid, and silently
// treating "./data" as a location would scatter data relative to whatever
// directory the process happened to start in.
func Resolve(env Env, home string) (Paths, error) {
	if strings.TrimSpace(home) == "" {
		return Paths{}, fmt.Errorf("cannot resolve %s: the user's home directory is unknown", DirName)
	}
	if !filepath.IsAbs(home) {
		return Paths{}, fmt.Errorf("home directory %q is not absolute", home)
	}
	absolute := func(name, fallback string) string {
		if value := strings.TrimSpace(env(name)); value != "" && filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(home, fallback)
	}
	paths := Paths{
		Config: absolute("XDG_CONFIG_HOME", ".config"),
		Data:   absolute("XDG_DATA_HOME", filepath.Join(".local", "share")),
		State:  absolute("XDG_STATE_HOME", filepath.Join(".local", "state")),
		Cache:  absolute("XDG_CACHE_HOME", ".cache"),
	}
	// XDG_RUNTIME_DIR has no default in the specification: its absence means
	// this session has no place for process-scoped files, not that some other
	// directory should be used instead.
	if value := strings.TrimSpace(env("XDG_RUNTIME_DIR")); value != "" && filepath.IsAbs(value) {
		paths.Runtime = value
	}
	paths.Config = filepath.Join(paths.Config, DirName)
	paths.Data = filepath.Join(paths.Data, DirName)
	paths.State = filepath.Join(paths.State, DirName)
	paths.Cache = filepath.Join(paths.Cache, DirName)
	if paths.Runtime != "" {
		paths.Runtime = filepath.Join(paths.Runtime, DirName)
	}
	return paths, nil
}

// RuntimeOrState is the directory for process-scoped files, falling back to
// state when the session has no runtime directory. The fallback is reported so
// the caller can say which one it used rather than leaving it to be guessed.
func (p Paths) RuntimeOrState() (string, bool) {
	if p.Runtime != "" {
		return p.Runtime, false
	}
	return p.State, true
}
