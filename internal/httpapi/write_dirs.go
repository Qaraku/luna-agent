package httpapi

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// writeDirsPath is the interface's own path for the directories a run may write
// in. It is not a capability route: which directories the user allows is data
// the user owns, and the composition root is what knows where it is stored.
const writeDirsPath = "/api/write-dirs"

// WriteDirPreference is the directories the user allows writing in, as this
// layer uses it: what is currently allowed, and how the whole list is replaced.
//
// The list is written as a whole rather than patched because the settings page owns
// it: a directory the page no longer names is gone, so a user can revoke one without
// having to track which request added it. Normalising the entries and collapsing
// duplicates belong to whoever stores them — this layer checks that each entry is
// usable and hands the list on.
type WriteDirPreference interface {
	// Dirs returns the directories that are currently allowed, as they are
	// stored.
	Dirs() ([]string, error)
	// SetDirs replaces the whole list. An error means nothing was written, and
	// no answer built from it may read as a save.
	SetDirs(dirs []string) error
}

// WithWriteDirs supplies the store the write-directory preference lives in. A
// server built without one reports that no write directories can be read or
// edited, rather than answering with an empty list: an empty list is a real
// choice ("no automatic write scope"), and a page that could not tell it apart from "nothing
// is wired up" would show the user a decision they never made.
func WithWriteDirs(pref WriteDirPreference) Option { return func(s *Server) { s.writeDirs = pref } }

// writeDirsResponse is the frozen shape of GET and PUT /api/write-dirs: the
// directories that may be written in, and nothing else. An empty list is written
// as an empty list, so a client never has to tell "none" apart from "not sent".
type writeDirsResponse struct {
	Dirs []string `json:"dirs"`
}

// getWriteDirs answers with the directories that are currently allowed.
//
// A server with no preference is an error rather than an empty list: see
// WithWriteDirs. A read that fails is reported with its reason, because a page
// showing directories that are not the stored ones is worse than a page that
// says it could not read them.
func (s *Server) getWriteDirs(w http.ResponseWriter) {
	dirs, err := s.readWriteDirs()
	if err != nil {
		fail(w, 500, err)
		return
	}
	send(w, 200, writeDirsResponse{Dirs: dirs})
}

// readWriteDirs reads the stored list, mapping "no preference was supplied" onto
// the same error the write path gives it. The wording is a sentence a person can
// act on: it names what is missing and does not pretend the answer is a list.
func (s *Server) readWriteDirs() ([]string, error) {
	if s.writeDirs == nil {
		return nil, fmt.Errorf("no write directory preference is configured")
	}
	dirs, err := s.writeDirs.Dirs()
	if err != nil {
		return nil, err
	}
	if dirs == nil {
		dirs = []string{}
	}
	return dirs, nil
}

// setWriteDirs stores the whole list the settings page submitted.
//
// The answer is the list read back after it was stored, never the request that
// was submitted: the store normalises and de-duplicates, so a page that echoed
// its own input would show the user a list that is not the one the next run
// obeys. A failure to write is a 500 with the reason — the page must not tell
// someone their directories were allowed when they were not — and a refusal of
// one entry is a 400 that names it.
func (s *Server) setWriteDirs(w http.ResponseWriter, r *http.Request) {
	if s.writeDirs == nil {
		fail(w, 500, fmt.Errorf("no write directory preference is configured"))
		return
	}
	var in struct {
		Dirs []string `json:"dirs"`
	}
	if !decode(w, r, &in) {
		return
	}
	dirs, err := validateWriteDirs(in.Dirs)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.writeDirs.SetDirs(dirs); err != nil {
		fail(w, 500, err)
		return
	}
	stored, err := s.readWriteDirs()
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.addEvent("write_dirs_saved", fmt.Sprintf("%d directories may be written in", len(stored)))
	send(w, 200, writeDirsResponse{Dirs: stored})
}

// validateWriteDirs checks the submitted entries one by one and names the one it
// refuses.
//
// An entry that cannot be used is never dropped quietly: dropping it would store
// a list that is not the one the page showed, and the user would be left
// believing a directory they added is allowed. Each entry is handed on trimmed,
// because a path with spaces around it names the same directory; normalising the
// path itself and collapsing duplicates are the store's business.
//
// An absolute path is required because the walk that bounds a write resolves a
// relative path against wherever the process happened to be started, which is not
// something the user chose. A NUL byte is refused because no path may contain one
// and a string that does cannot be handed to the operating system at all.
func validateWriteDirs(entries []string) ([]string, error) {
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		dir := strings.TrimSpace(entry)
		if dir == "" {
			return nil, fmt.Errorf("a directory in the form is empty")
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("the directory %q is not an absolute path", dir)
		}
		if strings.ContainsRune(dir, 0) {
			return nil, fmt.Errorf("the directory %q contains a NUL byte", dir)
		}
		dirs = append(dirs, dir)
	}
	return dirs, nil
}
