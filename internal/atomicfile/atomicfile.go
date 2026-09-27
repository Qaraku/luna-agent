// Package atomicfile rewrites a small file so that a reader never sees a
// half-written state.
//
// The technique is the ordinary one: write the whole new contents to a
// temporary file in the same directory, then rename it over the target. Until
// the rename happens every reader still sees the old contents, and after it
// every reader sees the new ones; there is no moment in between.
//
// It is its own package because more than one file in Luna is rewritten whole
// by the runtime itself — the user's preferences, and the workspaces they have
// defined — and a second hand-written copy of this sequence is exactly where
// one of them would quietly lose the "delete the temporary file on failure"
// step.
package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// dirMode is the permission given to a directory this package has to create.
// The directories it writes into hold the user's own configuration and data, so
// one that Luna creates itself is created private rather than world-readable.
const dirMode fs.FileMode = 0o700

// WriteFile replaces the contents of path with data.
//
// The parent directory is created when it is missing. mode is the permission of
// the resulting file, applied explicitly rather than inherited from the
// temporary file the standard library creates. A failure at any step removes
// the temporary file, so a write that did not happen leaves the directory
// exactly as it was.
//
// The error names the base names of the path and its directory, never the
// directory layout the host happens to have.
func WriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(dir), reason(err))
	}
	temp, err := os.CreateTemp(dir, name+".tmp-")
	if err != nil {
		return fmt.Errorf("write %s: %w", name, reason(err))
	}
	tempName := temp.Name()
	// CreateTemp already opens the file 0600; setting the mode explicitly keeps
	// the intent here instead of in the standard library's documentation.
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", name, reason(err))
	}
	_, writeErr := temp.Write(data)
	closeErr := temp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", name, reason(writeErr))
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", name, reason(err))
	}
	return nil
}

// reason reduces a path error to its underlying reason, so an error about a
// file does not echo the host's directory layout.
func reason(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
