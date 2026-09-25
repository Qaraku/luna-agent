// Package fileread owns what "reading a file" means in Luna.
//
// The two halves are deliberately split across the host/plugin boundary:
//
//   - Resolve runs on the host side and is the only place a model-supplied
//     path is interpreted. It normalizes the path, rejects absolute paths and
//     `..` escapes, resolves symbolic links, and refuses anything that is not a
//     regular file inside the read root. The host then hands the plugin the
//     already-resolved absolute path.
//   - Read runs on the plugin side and never interprets a path. It receives the
//     validated path plus the cap, reads at most one byte past the cap so an
//     oversize file is refused instead of truncated, and refuses binary
//     content.
//
// Error strings never contain an absolute host path: they are model-visible
// through tool.failed and, for a refusal, as the text of the tool result the
// model reads (see internal/agent), and the model already knows the relative
// path it asked for.
package fileread

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultLimit is the single-read size cap: 256 KiB.
const DefaultLimit = 256 << 10

// Rejections are sentinel errors so callers and tests can classify a refusal
// without matching on message text.
var (
	ErrPathEmpty     = errors.New("a path is required")
	ErrPathInvalid   = errors.New("malformed path")
	ErrPathAbsolute  = errors.New("absolute paths are not allowed; the path must be relative to the read root")
	ErrPathEscape    = errors.New("path escapes the read root")
	ErrPathOutside   = errors.New("path is outside the read root")
	ErrSymlinkEscape = errors.New("path leaves the read root through a symbolic link")
	ErrNotFound      = errors.New("file not found")
	ErrNotRegular    = errors.New("path is not a regular file")
	ErrTooLarge      = errors.New("file exceeds the single-read limit")
	ErrBinary        = errors.New("file is not text")
	ErrNotReadable   = errors.New("file cannot be read")
)

// Resolve validates a requested path against the read root and returns the
// absolute path of the regular file to read. root is a directory; requested is
// the raw, model-supplied path.
func Resolve(root, requested string, limit int) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", ErrPathEmpty
	}
	if strings.ContainsRune(requested, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrPathInvalid, requested)
	}
	if filepath.IsAbs(requested) {
		return "", fmt.Errorf("%w: %q", ErrPathAbsolute, requested)
	}
	joined := filepath.Join(root, filepath.FromSlash(requested))
	if !withinRoot(root, joined) {
		// The normalization above is the single containment mechanism, so the
		// diagnosis for the common cause is reported separately.
		if climbsAboveRoot(requested) {
			return "", fmt.Errorf("%w with a .. component: %q", ErrPathEscape, requested)
		}
		return "", fmt.Errorf("%w: %q", ErrPathOutside, requested)
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, requested)
	}
	if resolvedRoot, rootErr := filepath.EvalSymlinks(root); rootErr == nil && !withinRoot(resolvedRoot, resolved) {
		return "", fmt.Errorf("%w: %q", ErrSymlinkEscape, requested)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, requested)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q", ErrNotRegular, requested)
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if info.Size() > int64(limit) {
		return "", fmt.Errorf("%w: %q is %d bytes, over the %d-byte limit", ErrTooLarge, requested, info.Size(), limit)
	}
	return resolved, nil
}

// Read returns the text of an already-validated path. A path is never
// interpreted here: the caller passes the absolute path Resolve produced.
//
// Content is refused, not truncated, when it exceeds limit; the criteria are
// therefore explicit:
//
//   - size: at most limit bytes, checked against the bytes actually read, so a
//     file that grows after validation is still refused;
//   - text: any NUL (0x00) byte in the bytes actually read marks binary
//     content. This is the whole criterion; no charset heuristic is applied,
//     so valid UTF-8 with high bytes is accepted.
func Read(path string, limit int) (string, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotReadable, pathError(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotReadable, pathError(err))
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", ErrNotRegular, filepath.Base(path))
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotReadable, pathError(err))
	}
	if len(data) > limit {
		return "", fmt.Errorf("%w: at least %d bytes, over the %d-byte limit", ErrTooLarge, len(data), limit)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%w: it contains a NUL byte", ErrBinary)
	}
	return string(data), nil
}

// pathError reduces an *os.PathError to its cause so an absolute host path
// never reaches the model through an error string.
func pathError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// withinRoot reports whether path is root itself or lies strictly below it. It
// compares cleaned absolute paths, so a sibling that merely shares a name
// prefix (…/repo-other next to …/repo) is not treated as inside, and a relative
// or empty root never acts as a prefix.
func withinRoot(root, path string) bool {
	if root == "" || path == "" || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	if root == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// climbsAboveRoot reports whether a path uses enough `..` components to rise
// above the directory it is resolved against. A `..` that stays inside, such as
// docs/../docs/a.md, is not an escape.
func climbsAboveRoot(requested string) bool {
	depth := 0
	for _, part := range strings.Split(filepath.ToSlash(requested), "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}
