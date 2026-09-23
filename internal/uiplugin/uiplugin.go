// Package uiplugin owns runtime UI plugins: the plugins/ui/<name>/ directory
// convention, the manifest that declares one, and serving one file from inside
// a plugin directory.
//
// The two halves are deliberately separate:
//
//   - List reads the manifests under the UI plugin root and is never fatal for
//     one directory. A plugin whose manifest is missing, malformed or does not
//     match its own directory is skipped and reported, so one broken plugin can
//     neither empty the list nor fail the request that asked for it.
//   - File validates one requested file for one plugin name and returns its
//     content plus the Content-Type to serve it with. Path normalization and
//     symbolic-link containment are fileread's checks, not a second, weaker
//     copy of them: the request is resolved against the plugin's own directory,
//     so after normalization and after resolving links the file must still be
//     inside it.
//
// No browser-supplied string is used as a path. A plugin name is a single
// directory name matching [a-z0-9-]{1,32}, and a file is a relative path
// validated against that plugin's directory. Error strings never contain an
// absolute host path: they are browser-visible through the API.
package uiplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Qaraku/luna-agent/internal/fileread"
)

// Dir is the directory under the repository root that holds UI plugins.
const Dir = "ui"

// manifestFile is the file a plugin directory declares itself with.
const manifestFile = "plugin.json"

// namePattern is the whole shape a plugin name may have. It admits no path
// separator, no dot and no `..`, so a name is only ever one directory name
// under the UI plugin root.
var namePattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// contentTypes is the complete set of extensions this package serves. The table
// is closed on purpose: an extension that is not here is refused rather than
// guessed, and a response is never produced from content sniffing.
var contentTypes = map[string]string{
	".js":   "text/javascript",
	".json": "application/json",
}

// Refusals raised by this package. A path that escapes, leaves the plugin root
// through a link or does not exist is rejected with fileread's own sentinels,
// so both capabilities classify a refusal the same way.
var (
	ErrNameInvalid = errors.New("plugin name must match [a-z0-9-]{1,32}")
	ErrFileType    = errors.New("file type is not served")
)

// errRootUnset reports a host that has no UI plugin directory configured. It is
// a host state and not a client error: it lists no plugins and serves no file.
var errRootUnset = errors.New("ui plugin root is not configured")

// Skip reasons are fixed phrases safe to show a browser: they name no host path
// and no file content.
const (
	ReasonNameInvalid        = "plugin directory name is not a valid plugin name"
	ReasonManifestMissing    = "plugin.json is missing"
	ReasonManifestUnreadable = "plugin.json cannot be read from inside the plugin directory"
	ReasonManifestMalformed  = "plugin.json is not valid JSON"
	ReasonNameMismatch       = "plugin.json name does not match its directory"
	ReasonFieldMissing       = "plugin.json is missing name, title or entry"
	ReasonEntryInvalid       = "plugin.json entry is not a relative path inside the plugin"
)

// Manifest is the plugin.json contract: what a plugin declares about itself.
// Description is optional; name, title and entry are required, and name must
// equal the directory the manifest lives in.
type Manifest struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Entry       string `json:"entry"`
}

// Skipped is one directory that List did not return as a plugin, with the
// reason it was skipped.
type Skipped struct {
	Name   string
	Reason string
}

// ValidName reports whether name is a usable plugin name. It is the only shape
// a plugin name may have, so a name can never carry a separator, a dot or `..`.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ContentType returns the Content-Type a file is served with and whether this
// package serves that extension at all. The extension is matched
// case-insensitively, so the spelling of an extension cannot move a file
// between "served" and "refused".
func ContentType(name string) (string, bool) {
	contentType, ok := contentTypes[strings.ToLower(filepath.Ext(name))]
	return contentType, ok
}

// List returns the plugins discoverable under root, sorted by name, together
// with the directories that were skipped. A skipped directory is not an error:
// one unusable manifest must not hide the healthy plugins or fail the request.
//
// A root that is unset or does not exist holds no plugins and is not an error;
// a root that exists but cannot be read is.
func List(root string) ([]Manifest, []Skipped, error) {
	manifests := []Manifest{}
	skipped := []Skipped{}
	dir, err := resolveRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errRootUnset) {
			return manifests, skipped, nil
		}
		return nil, nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("ui plugin directory cannot be listed: %w", cause(err))
	}
	for _, entry := range entries {
		// Only a real directory under the root is a plugin candidate. A file
		// such as a README is not a plugin, and a symbolic link is never
		// reported as a directory, so neither is a candidate.
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !ValidName(name) {
			skipped = append(skipped, Skipped{Name: name, Reason: ReasonNameInvalid})
			continue
		}
		manifest, reason := readManifest(filepath.Join(dir, name), name)
		if reason != "" {
			skipped = append(skipped, Skipped{Name: name, Reason: reason})
			continue
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Name < manifests[j].Name })
	return manifests, skipped, nil
}

// File validates one request for one file of one plugin and returns its content
// and the Content-Type to serve it with. Every check fails closed:
//
//   - name must match [a-z0-9-]{1,32}, so it is one directory name and never a
//     path;
//   - the extension must be one this package serves, checked before the
//     filesystem is touched at all;
//   - the plugin's directory must be a real directory under the root: a
//     symbolic link in its place would move the containment root the file check
//     resolves against;
//   - the file is resolved with fileread.Resolve against that plugin's
//     directory, so after normalization and after resolving links it must still
//     be inside that directory, must be a regular file and must fit the size
//     cap;
//   - the content is read with fileread.Read, which refuses to truncate an
//     oversize file and refuses binary content.
func File(root, name, file string) (string, string, error) {
	if !ValidName(name) {
		return "", "", fmt.Errorf("%w: %q", ErrNameInvalid, name)
	}
	contentType, ok := ContentType(file)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrFileType, filepath.Ext(file))
	}
	dir, err := resolveRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errRootUnset) {
			return "", "", fmt.Errorf("%w: plugin %q", fileread.ErrNotFound, name)
		}
		return "", "", err
	}
	pluginDir := filepath.Join(dir, name)
	if !realDirectory(pluginDir) {
		return "", "", fmt.Errorf("%w: plugin %q", fileread.ErrNotFound, name)
	}
	path, err := fileread.Resolve(pluginDir, file, fileread.DefaultLimit)
	if err != nil {
		return "", "", fmt.Errorf("plugin %q: %w", name, err)
	}
	content, err := fileread.Read(path, fileread.DefaultLimit)
	if err != nil {
		return "", "", fmt.Errorf("plugin %q: %w", name, err)
	}
	return content, contentType, nil
}

// readManifest reads and validates one plugin's manifest. An unusable plugin
// yields a reason rather than an error, because it is a skip and not a failure.
//
// The manifest is resolved with fileread.Resolve like any other plugin file, so
// a manifest that is a link out of the plugin directory is refused instead of
// read.
func readManifest(dir, name string) (Manifest, string) {
	path, err := fileread.Resolve(dir, manifestFile, fileread.DefaultLimit)
	if err != nil {
		if errors.Is(err, fileread.ErrNotFound) {
			return Manifest{}, ReasonManifestMissing
		}
		return Manifest{}, ReasonManifestUnreadable
	}
	data, err := fileread.Read(path, fileread.DefaultLimit)
	if err != nil {
		// Oversize or binary: the manifest cannot be used either way, and the
		// caller does not need the distinction.
		return Manifest{}, ReasonManifestUnreadable
	}
	// Unknown fields are ignored on purpose: a manifest may carry a key this
	// core does not know yet without becoming unusable.
	var manifest Manifest
	if err := json.Unmarshal([]byte(data), &manifest); err != nil {
		return Manifest{}, ReasonManifestMalformed
	}
	if manifest.Name != name {
		return Manifest{}, ReasonNameMismatch
	}
	manifest.Title = strings.TrimSpace(manifest.Title)
	manifest.Entry = strings.TrimSpace(manifest.Entry)
	if manifest.Title == "" || manifest.Entry == "" {
		return Manifest{}, ReasonFieldMissing
	}
	if !entryPath(manifest.Entry) {
		return Manifest{}, ReasonEntryInvalid
	}
	return manifest, ""
}

// entryPath reports whether a manifest entry is a path this package may serve:
// a relative path that stays inside the plugin directory. filepath.IsLocal is
// the same rule fileread applies before it touches the filesystem, plus the NUL
// check IsLocal does not make.
func entryPath(entry string) bool {
	return filepath.IsLocal(entry) && !strings.ContainsRune(entry, 0)
}

// resolveRoot makes root absolute and resolves its symbolic links, so every
// containment comparison compares real directories rather than links.
func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errRootUnset
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("ui plugin root is not usable: %w", cause(err))
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("ui plugin root is not usable: %w", cause(err))
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("ui plugin root is not usable: %w", cause(err))
	}
	if !info.IsDir() {
		return "", errors.New("ui plugin root is not a directory")
	}
	return resolved, nil
}

// realDirectory reports whether path is a real directory. Lstat reports a
// symbolic link as a link and never as a directory, so a linked plugin
// directory is refused rather than followed.
func realDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// cause reduces an *os.PathError to its bare cause, so an absolute host path
// never reaches a caller through an error string while errors.Is still
// classifies the failure.
func cause(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
