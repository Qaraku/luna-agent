// Package fileread owns what "reading a file" and "listing a directory" mean in
// Luna.
//
// The two halves are deliberately split across the host/plugin boundary:
//
//   - Resolve and ResolveDir run on the host side and are the only place a
//     model-supplied path is interpreted. They normalize the path, reject
//     absolute paths and `..` escapes, resolve symbolic links, and refuse
//     anything that does not end up inside the read root — Resolve the regular
//     file to read, ResolveDir the directory to list. Both share the same
//     resolution and containment code, so the boundary has one implementation
//     whatever the tool does with the path. The host then hands the plugin the
//     already-resolved absolute path.
//   - Read and List run on the plugin side and never interpret a path. They
//     receive the validated path plus the caps, and both refuse to overstate
//     what they found: Read reads at most one byte past the cap so an oversize
//     file is refused instead of truncated and refuses binary content, and List
//     renders exactly one level — it never enters a subdirectory and never
//     follows a symbolic link — and states every cap it hit instead of cutting
//     the list silently.
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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DefaultLimit is the single-read size cap: 256 KiB.
const DefaultLimit = 256 << 10

// The listing caps. At most DefaultListEntries entries are rendered in one
// listing, and one rendered line is at most DefaultListLineBytes bytes. They
// bound a listing the model has to read for the same reason DefaultLimit bounds
// a read: a result too large to digest is not a truthful result either.
const (
	DefaultListEntries   = 200
	DefaultListLineBytes = 160
)

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
	ErrNotDir        = errors.New("path is not a directory")
	ErrTooLarge      = errors.New("file exceeds the single-read limit")
	ErrBinary        = errors.New("file is not text")
	ErrNotReadable   = errors.New("file cannot be read")
	ErrNotListable   = errors.New("directory cannot be listed")
)

// Resolve validates a requested file path against the read root and returns the
// absolute path of the regular file to read. root is a directory; requested is
// the raw, model-supplied path.
func Resolve(root, requested string, limit int) (string, error) {
	resolved, err := resolveWithinRoot(root, requested)
	if err != nil {
		return "", err
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

// ResolveDir validates a requested directory path against the read root and
// returns its absolute path. It resolves and contains the path with exactly the
// code Resolve uses, so a listing and a read cannot disagree about where the
// read root ends; what differs is only what the resolved path has to be.
func ResolveDir(root, requested string) (string, error) {
	resolved, err := resolveWithinRoot(root, requested)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, requested)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %q", ErrNotDir, requested)
	}
	return resolved, nil
}

// resolveWithinRoot is the single place a model-supplied path is interpreted:
// normalization, containment, and symbolic-link resolution all happen here, so
// every caller shares one boundary rather than re-deriving it. It returns the
// resolved absolute path without saying what it is; the callers above apply
// their own requirement (a regular file, a directory).
func resolveWithinRoot(root, requested string) (string, error) {
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

// ListOptions carries the host's caps for one listing. A non-positive cap falls
// back to the package default, so a plugin can never render an unbounded
// listing just because a host sent no cap.
type ListOptions struct {
	// MaxEntries is the largest number of entries one listing renders.
	MaxEntries int
	// MaxLineBytes is the largest length of one rendered line.
	MaxLineBytes int
	// ExactBytes renders every file size as an exact byte count instead of a
	// human-readable unit. Both renderings round the same measured number, so
	// neither invents a size; the choice is what makes a candidate replacement
	// observable in the tool result.
	ExactBytes bool
}

// listEntry is one directory entry as it will be rendered.
type listEntry struct {
	kind string
	size string
	name string
}

// List renders one level of dir. dir is an absolute path the host already
// validated against the read root: no path is interpreted here, and no entry is
// ever entered. The result is bounded three ways, and each bound is stated in
// the text rather than hidden:
//
//   - one level only, so a listing can never become a recursive dump;
//   - at most opts.MaxEntries entries rendered, with the remainder counted in a
//     closing line;
//   - at most opts.MaxLineBytes bytes per line, with a cut name marked and its
//     real length reported.
//
// Order is fixed and part of the result's meaning: directories first, then files
// and links, each group keeping the name order the directory read returned.
func List(dir string, opts ListOptions) (string, error) {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultListEntries
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = DefaultListLineBytes
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// The path is absolute here, so only the cause is reported.
		return "", fmt.Errorf("%w: %s", ErrNotListable, pathError(err))
	}
	items := make([]listEntry, 0, len(entries))
	counts := map[string]int{}
	for _, e := range entries {
		item := classify(e, opts.ExactBytes)
		counts[item.kind]++
		items = append(items, item)
	}
	groups := []string{"dir", "file", "other", "link"}
	ordered := make([]listEntry, 0, len(items))
	for _, kind := range groups {
		for _, item := range items {
			if item.kind == kind {
				ordered = append(ordered, item)
			}
		}
	}

	var b strings.Builder
	parts := make([]string, 0, len(groups))
	for _, kind := range groups {
		if counts[kind] > 0 {
			parts = append(parts, plural(counts[kind], kind))
		}
	}
	if len(parts) == 0 {
		b.WriteString("0 entries: the directory is empty\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "%s: %s (directories first, then files and links, each by name)\n", plural(len(entries), "entry"), strings.Join(parts, ", "))
	shown := 0
	for _, item := range ordered {
		if shown == opts.MaxEntries {
			break
		}
		shown++
		prefix := fmt.Sprintf("%-4s %9s  ", item.kind, item.size)
		b.WriteString(fitLine(prefix, item.name, opts.MaxLineBytes))
		b.WriteString("\n")
	}
	if remaining := len(ordered) - shown; remaining > 0 {
		fmt.Fprintf(&b, "%s are not listed: one listing returns at most %s\n", plural(remaining, "entry"), plural(opts.MaxEntries, "entry"))
	}
	return b.String(), nil
}

// classify states what one entry is, from its own metadata. A symbolic link is
// never followed: its target may be outside the read root, and what lstat
// reports for it is the length of the target path rather than a size, so a link
// carries no size at all instead of a misleading one.
func classify(entry fs.DirEntry, exact bool) listEntry {
	info, err := entry.Info()
	if err != nil {
		// The entry changed while it was being listed. It is reported as an
		// unknown kind rather than as something it may no longer be.
		return listEntry{kind: "other", size: "-", name: entry.Name()}
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		return listEntry{kind: "dir", size: "-", name: entry.Name()}
	case mode&fs.ModeSymlink != 0:
		return listEntry{kind: "link", size: "-", name: entry.Name()}
	case mode.IsRegular():
		return listEntry{kind: "file", size: renderSize(info.Size(), exact), name: entry.Name()}
	default:
		return listEntry{kind: "other", size: "-", name: entry.Name()}
	}
}

// renderSize states a measured size. exact asks for the raw byte count; the
// human-readable units are the same number in another unit, never an estimate.
func renderSize(size int64, exact bool) string {
	switch {
	case exact || size < 1<<10:
		return fmt.Sprintf("%d B", size)
	case size < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	case size < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	default:
		return fmt.Sprintf("%.1f GiB", float64(size)/(1<<30))
	}
}

// fitLine renders one entry line inside maxLineBytes. A name that does not fit is
// cut and the line says that it was cut and how long the real name is: a listing
// whose lines were cut silently would misstate the directory it describes.
func fitLine(prefix, name string, maxLineBytes int) string {
	if len(prefix)+len(name) <= maxLineBytes {
		return prefix + name
	}
	note := fmt.Sprintf("…(name truncated; it is %d bytes)", len(name))
	if budget := maxLineBytes - len(prefix) - len(note); budget >= 1 {
		return prefix + clip(name, budget) + note
	}
	// The cap is too small for the note; the marker alone still says the name is
	// incomplete.
	budget := maxLineBytes - len(prefix) - len(ellipsis)
	if budget < 0 {
		budget = 0
	}
	return prefix + clip(name, budget) + ellipsis
}

// ellipsis marks a name that did not fit. It is also the shortest note fitLine
// can write.
const ellipsis = "…"

// clip returns the first budget bytes of s without splitting a UTF-8 rune. A
// name that is not valid UTF-8 — a POSIX name need not be — is cut on the byte
// boundary the budget asks for, so nothing beyond the budget is dropped.
func clip(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if len(s) <= budget {
		return s
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// plural renders a count with its noun so model-visible text never says
// "1 entries".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	if strings.HasSuffix(noun, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(noun, "y"))
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
