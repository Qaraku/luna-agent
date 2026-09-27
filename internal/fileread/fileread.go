// Package fileread owns what "reading a file", "listing a directory",
// "searching for text" and "finding an entry by name" mean in Luna.
//
// The two halves are deliberately split across the host/plugin boundary:
//
//   - Resolve, ResolveDir and ResolveSearch run on the host side and are the
//     only place a model-supplied path is interpreted. They normalize the path,
//     reject absolute paths and `..` escapes, resolve symbolic links, and refuse
//     anything that does not end up inside the read root — Resolve the regular
//     file to read, ResolveDir the directory to list, ResolveSearch the file or
//     directory to search or to find in. All three share the same resolution and
//     containment code, so the boundary has one implementation whatever the tool
//     does with the path. The host then hands the plugin the already-resolved
//     absolute path.
//   - Read, List, Search and Find run on the plugin side and never interpret a
//     path. They receive the validated path plus the caps, and all four refuse to
//     overstate what they found: Read reads at most one byte past the cap so an
//     oversize file is refused instead of truncated and refuses binary content,
//     List renders exactly one level — it never enters a subdirectory and never
//     follows a symbolic link — and states every cap it hit instead of cutting
//     the list silently, Search matches one literal per line, never follows
//     a symbolic link, and states every cap it hit — including that its walk
//     stopped, because a search that stopped cannot say how many matches it did
//     not find — and Find matches one glob against one entry name, never enters
//     a symbolic link, and states every cap it hit, including that its walk
//     stopped, for the same reason.
//
// Error strings never contain an absolute host path: they are model-visible
// through tool.failed and, for a refusal, as the text of the tool result the
// model reads (see internal/agent), and the model already knows the relative
// path it asked for.
package fileread

import (
	"bufio"
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

// The search caps. One search renders at most DefaultSearchMatches matching
// lines in lines of at most DefaultSearchLineBytes bytes, reads at most
// DefaultSearchFiles files, and reads no file larger than DefaultSearchFileBytes.
// They bound a search for the same reason DefaultLimit bounds a read: a result
// too large to digest is not a truthful result either.
const (
	DefaultSearchMatches   = 100
	DefaultSearchLineBytes = 200
	DefaultSearchFiles     = 2000
	DefaultSearchFileBytes = DefaultLimit
)

// MaxQueryBytes is the longest literal one search accepts. A longer literal is
// refused rather than cut, because a cut query would match text the model never
// asked about.
const MaxQueryBytes = 256

// Rejections are sentinel errors so callers and tests can classify a refusal
// without matching on message text.
var (
	ErrPathEmpty    = errors.New("a path is required")
	ErrPathInvalid  = errors.New("malformed path")
	ErrPathAbsolute = errors.New("absolute paths are not allowed; the path must be relative to the read root")
	ErrPathEscape   = errors.New("path escapes the read root")
	ErrPathOutside  = errors.New("path is outside the read root")
	// ErrNoRoot reports a multi-root check that was given no root at all. The
	// caller decides what its default root is; an empty set reaching the
	// boundary is a caller mistake, not a request to search everywhere.
	ErrNoRoot        = errors.New("no read root was given")
	ErrSymlinkEscape = errors.New("path leaves the read root through a symbolic link")
	ErrNotFound      = errors.New("file not found")
	ErrNotRegular    = errors.New("path is not a regular file")
	ErrNotDir        = errors.New("path is not a directory")
	ErrTooLarge      = errors.New("file exceeds the single-read limit")
	ErrBinary        = errors.New("file is not text")
	// ErrRangeInvalid reports a line range that is not a range: a negative
	// start_line or max_lines. Zero is not a range either — it is how the
	// protocol says "not asked for" — so a negative value is refused and
	// explained rather than guessed at.
	ErrRangeInvalid = errors.New("the line range is not valid")
	// ErrStartLinePastEnd reports a start_line beyond the last line of the
	// file. It is a refusal, not an empty result: the caller asked for a part
	// of a file that does not exist, and what it gets instead is the length of
	// the file it was wrong about.
	ErrStartLinePastEnd = errors.New("start_line is past the last line of the file")
	// ErrLineTooLarge reports a range whose very first line does not fit
	// inside the byte limit on its own. No line is ever returned cut, so there
	// is no honest partial answer here: the call is refused.
	ErrLineTooLarge = errors.New("the first line of the range is longer than the single-read limit")
	ErrNotReadable  = errors.New("file cannot be read")
	ErrNotListable  = errors.New("directory cannot be listed")
	// ErrNotSearchable reports a search whose starting path is neither a
	// regular file nor a directory — or is a symbolic link, which a search
	// never follows.
	ErrNotSearchable = errors.New("path is neither a regular file nor a directory")
	// ErrQueryEmpty reports an empty (or whitespace-only) literal, which would
	// match every line.
	ErrQueryEmpty = errors.New("a literal query is required")
	// ErrQueryTooLarge reports a literal longer than MaxQueryBytes.
	ErrQueryTooLarge = errors.New("the query is longer than the limit")
	// ErrQueryInvalid reports a literal the search cannot represent: one with a
	// NUL byte, or one with a line break, since a match is decided within one
	// line and such a literal could never match anything.
	ErrQueryInvalid = errors.New("the query must be one line of text without NUL bytes")
)

// Resolve validates a requested file path against the read root and returns the
// absolute path of the regular file to read. root is a directory; requested is
// the raw, model-supplied path.
func Resolve(root, requested string, limit int) (string, error) {
	resolved, info, err := resolveRegularFile(root, requested)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if info.Size() > int64(limit) {
		return "", fmt.Errorf("%w: %q is %d bytes, over the %d-byte limit", ErrTooLarge, requested, info.Size(), limit)
	}
	return resolved, nil
}

// ResolveRange validates a requested file path for a range read. It resolves,
// contains and symbolic-link-resolves the path with exactly the code Resolve
// uses, so a range read cannot reach anywhere a whole-file read could not;
// what differs is only that the size of the file is not a refusal here. A range
// read exists because a file can be too large to read whole, and what bounds
// the answer is the byte limit of the window that comes back, not the size of
// the file being looked at.
func ResolveRange(root, requested string) (string, error) {
	resolved, _, err := resolveRegularFile(root, requested)
	return resolved, err
}

// resolveRegularFile is the part of a read that only says where the path is:
// normalize, contain and resolve symbolic links (resolveWithinRoot), then
// require an existing regular file. Every file-read entry point is built on it
// so that no two reads can be looking at different files, and a new entry point
// cannot quietly get a weaker boundary by writing its own.
func resolveRegularFile(root, requested string) (string, os.FileInfo, error) {
	resolved, err := resolveWithinRoot(root, requested)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %q", ErrNotFound, requested)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %q", ErrNotRegular, requested)
	}
	return resolved, info, nil
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

// ResolveSearch validates a requested path against the read root and returns
// the absolute path of the existing file or directory to search. It resolves and
// contains the path with exactly the code Resolve and ResolveDir use, so a
// search cannot start anywhere a read or a listing could not reach; what differs
// is only what the resolved path has to be — a search starts either at one file
// or at one directory — and that the plugin's walk is what bounds the rest.
func ResolveSearch(root, requested string) (string, error) {
	resolved, err := resolveWithinRoot(root, requested)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, requested)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q", ErrNotSearchable, requested)
	}
	return resolved, nil
}

// Resolved is a path a multi-root check accepted, together with the root that
// accepted it. One request may serve several working directories, so a caller
// that has to say where a path was found is told rather than left to guess.
type Resolved struct {
	// Path is the resolved absolute path: what Resolve, ResolveDir and
	// ResolveSearch return for one root.
	Path string
	// Root is the root that admitted Path, exactly as it was given to the call.
	Root string
}

// ResolveInRoots is Resolve against several roots: the first root that holds
// the path wins. See resolveInRoots for what "first" and "holds" mean.
func ResolveInRoots(roots []string, requested string, limit int) (Resolved, error) {
	return resolveInRoots(roots, requested, func(root string) (string, error) {
		return Resolve(root, requested, limit)
	})
}

// ResolveDirInRoots is ResolveDir against several roots, on the same terms as
// ResolveInRoots: every root is checked by the code a single-root call uses, so
// a listing and a read cannot disagree about where the roots end.
func ResolveDirInRoots(roots []string, requested string) (Resolved, error) {
	return resolveInRoots(roots, requested, func(root string) (string, error) {
		return ResolveDir(root, requested)
	})
}

// ResolveRangeInRoots is ResolveRange against several roots, on the same terms
// as ResolveInRoots: the first root that holds the path wins, and every root is
// checked by the code a single-root call uses, so a range read and a whole-file
// read cannot disagree about where the roots end.
func ResolveRangeInRoots(roots []string, requested string) (Resolved, error) {
	return resolveInRoots(roots, requested, func(root string) (string, error) {
		return ResolveRange(root, requested)
	})
}

// ResolveSearchInRoots is ResolveSearch against several roots, on the same
// terms as ResolveInRoots: a search cannot start anywhere a read or a listing
// could not reach, under any of the roots it was given.
func ResolveSearchInRoots(roots []string, requested string) (Resolved, error) {
	return resolveInRoots(roots, requested, func(root string) (string, error) {
		return ResolveSearch(root, requested)
	})
}

// ValidateQuery checks one literal query before any work is done with it. It
// lives here, next to the search that consumes it, so the host can refuse a
// query on the same terms the plugin would: an empty literal matches every line
// and says nothing, and a literal the search cannot represent — with a NUL byte,
// or with a line break, since a match is decided within one line — would come
// back as an empty result that looked like an answer.
func ValidateQuery(query string) error {
	switch {
	case strings.TrimSpace(query) == "":
		return ErrQueryEmpty
	case len(query) > MaxQueryBytes:
		return fmt.Errorf("%w: %d bytes, over the %d-byte limit", ErrQueryTooLarge, len(query), MaxQueryBytes)
	case strings.ContainsRune(query, 0):
		return fmt.Errorf("%w: it contains a NUL byte", ErrQueryInvalid)
	case strings.ContainsAny(query, "\n\r"):
		return fmt.Errorf("%w: it contains a line break", ErrQueryInvalid)
	}
	return nil
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

// resolveInRoots runs one single-root check against each root in turn and
// returns the first root that accepts the path.
//
// It is a loop over the checks above and not a second boundary: every root is
// checked by resolveWithinRoot, through Resolve, ResolveDir or ResolveSearch, so
// normalization, the refusal of an absolute path and the symbolic-link
// resolution are the same code a single-root call runs, and adding a root here
// cannot make any of them looser. What differs is only which root is asked.
//
// The one decision made before the loop is the `..` climb, because it is the
// only refusal that could be undone by trying another root. A path whose `..`
// components rise above the root it is resolved against is an escape, and that
// is a property of the requested path alone (see climbsAboveRoot) rather than of
// any one root. Left inside the loop it would make the answer depend on the
// order the roots were given: `../b/file` escapes root a with a `..` component
// and is refused there, but it is also a legal relative path under root b — a
// sibling directory — so the loop would accept it as soon as b was reached.
// Decided once, a path that climbs above a root is refused even when another
// root given in the same call would contain it. An absolute path needs no such
// handling here: it is a refusal the per-root check makes for every root.
//
// The other refusals are per root and are simply moved past, so a name that
// exists in one of the working directories is found rather than reported as
// missing because a different directory was consulted first. When no root
// accepts it, the first root's refusal is reported: it is the reading of the
// call that was asked for first.
func resolveInRoots(roots []string, requested string, check func(root string) (string, error)) (Resolved, error) {
	if len(roots) == 0 {
		return Resolved{}, ErrNoRoot
	}
	if !filepath.IsAbs(requested) && climbsAboveRoot(requested) {
		return Resolved{}, fmt.Errorf("%w with a .. component: %q", ErrPathEscape, requested)
	}
	var firstErr error
	for _, root := range roots {
		resolved, err := check(root)
		if err == nil {
			return Resolved{Path: resolved, Root: root}, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return Resolved{}, firstErr
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

// lineScanBuffer is the size of the buffer one range read walks a file with. A
// line longer than it is read in as many pieces as it takes; nothing is decided
// about a line until its end is seen.
const lineScanBuffer = 64 << 10

// RangeOptions carries one bounded line-range read of a file. A zero StartLine
// means the first line, a zero MaxLines means every line from there on, and a
// zero MaxBytes means DefaultLimit.
//
// These bounds are on the result, not on the file. A range read exists so that
// a file too large to read whole can still be read in part, so the size of the
// file is deliberately not a refusal here: what comes back is at most MaxBytes
// of at most MaxLines lines, and it says which lines it covers and what it left
// unread.
type RangeOptions struct {
	// StartLine is the 1-based number of the first line to return.
	StartLine int
	// MaxLines is the largest number of lines to return.
	MaxLines int
	// MaxBytes is the largest number of bytes to return.
	MaxBytes int
}

// asksRange reports whether these options ask for a part of the file at all.
// With neither counter set the call is a whole-file read and is answered with
// exactly what Read returns.
func (o RangeOptions) asksRange() bool { return o.StartLine > 0 || o.MaxLines > 0 }

// ValidateRange checks the two optional line-range parameters before any work is
// done with them. Zero means "not asked for" in the plugin protocol, so a
// negative counter is a malformed call and is refused with the value it was
// given; a caller is never left to guess what a negative start_line meant.
func ValidateRange(startLine, maxLines int) error {
	if startLine < 0 {
		return fmt.Errorf("%w: start_line must be 1 or greater, or 0 for the whole file (got %d)", ErrRangeInvalid, startLine)
	}
	if maxLines < 0 {
		return fmt.Errorf("%w: max_lines must be 1 or greater, or 0 for every line from start_line on (got %d)", ErrRangeInvalid, maxLines)
	}
	return nil
}

// ReadRange reads a range of path and returns the model-visible result: a
// header line stating which lines the text covers, followed by those lines.
// A call that asks for no range returns the whole file with no header, exactly
// as Read does.
func ReadRange(path string, opts RangeOptions) (string, error) {
	header, text, err := ReadRangeParts(path, opts)
	if err != nil {
		return "", err
	}
	return header + text, nil
}

// ReadRangeParts is ReadRange split into its two halves, so a candidate that
// rewrites the file text — v2 normalizes line endings — can rewrite that half
// without touching the header that states the range. The criteria for "a text
// file that may be read" are Read's criteria, applied by the same scan: at most
// the byte limit of content comes back, and any NUL byte in the file refuses the
// whole call as binary, so a file the model could not read whole is not one it
// can read in part either.
//
// The scan is bounded in memory, never in reads. One fixed buffer holds the
// bytes coming off the file, one holds the line being appended to — a line is
// kept whole or left out whole, never cut — and one grows to at most MaxBytes
// and holds the answer. The file itself is never held: it is walked one line at
// a time to the end, because the number of lines is what the header has to
// state and a NUL byte anywhere is what refuses the call. What that costs is a
// full read of the file; what it buys is an honest answer about a file that is
// larger than the single-read limit.
func ReadRangeParts(path string, opts RangeOptions) (header, text string, err error) {
	if err := ValidateRange(opts.StartLine, opts.MaxLines); err != nil {
		return "", "", err
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultLimit
	}
	if !opts.asksRange() {
		text, err = Read(path, opts.MaxBytes)
		return "", text, err
	}
	start := opts.StartLine
	if start == 0 {
		start = 1
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s", ErrNotReadable, pathError(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", fmt.Errorf("%w: %s", ErrNotReadable, pathError(err))
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: %s", ErrNotRegular, filepath.Base(path))
	}

	reader := bufio.NewReaderSize(f, lineScanBuffer)
	window := make([]byte, 0, 64<<10)
	pending := make([]byte, 0, 256)
	lines := 0 // completed lines: the length of the file in lines
	kept := 0  // lines held in window
	first, last := 0, 0
	keep := true  // whether the line being read may still be kept
	over := false // the line being read does not fit in what is left of the limit
	stoppedLines, stoppedBytes := false, false

	// closeLine ends the line being read: it counts it, and either keeps it
	// whole or states why the read stops before it.
	closeLine := func() error {
		lines++
		if keep && lines >= start {
			switch {
			case opts.MaxLines > 0 && kept >= opts.MaxLines:
				keep, stoppedLines = false, true
			case over && kept == 0:
				return fmt.Errorf("%w: line %d alone is longer than the %d-byte limit", ErrLineTooLarge, lines, opts.MaxBytes)
			case over:
				keep, stoppedBytes = false, true
			default:
				if kept == 0 {
					first = lines
				}
				window = append(window, pending...)
				last = lines
				kept++
			}
		}
		pending = pending[:0]
		over = false
		return nil
	}

	walk := func() error {
		for {
			chunk, readErr := reader.ReadSlice('\n')
			if len(chunk) > 0 {
				if bytes.IndexByte(chunk, 0) >= 0 {
					return fmt.Errorf("%w: it contains a NUL byte", ErrBinary)
				}
				if keep && !over {
					if len(pending)+len(chunk) > opts.MaxBytes-len(window) {
						over = true
					} else {
						pending = append(pending, chunk...)
					}
				}
			}
			switch readErr {
			case nil:
				if err := closeLine(); err != nil {
					return err
				}
			case bufio.ErrBufferFull:
				// The line is longer than the scan buffer: keep reading it.
				// Nothing about it is decided until its end is reached.
			case io.EOF:
				if len(chunk) > 0 {
					// The file does not end with a newline, so its last line
					// ends here.
					return closeLine()
				}
				return nil
			default:
				return fmt.Errorf("%w: %s", ErrNotReadable, pathError(readErr))
			}
		}
	}
	if err := walk(); err != nil {
		return "", "", err
	}

	if start > lines {
		return "", "", fmt.Errorf("%w: start_line %d is past the end of the file, which has %s", ErrStartLinePastEnd, start, plural(lines, "line"))
	}
	text = string(window)
	if remaining := lines - last; remaining > 0 {
		note := "the read stopped before the end of the file"
		switch {
		case stoppedLines:
			note = fmt.Sprintf("%s stopped the read", plural(opts.MaxLines, "line"))
		case stoppedBytes:
			note = fmt.Sprintf("the %d-byte limit stopped the read", opts.MaxBytes)
		}
		return fmt.Sprintf("lines %d-%d of %d (%s; the remaining %s were not read)\n", first, last, lines, note, plural(remaining, "line")), text, nil
	}
	return fmt.Sprintf("lines %d-%d of %d\n", first, last, lines), text, nil
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
		b.WriteString(fitLine(prefix, item.name, opts.MaxLineBytes, "name"))
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
// whose lines were cut silently would misstate the directory it describes. noun
// names what was cut, so a search can state the same thing about a matched line.
func fitLine(prefix, name string, maxLineBytes int, noun string) string {
	if len(prefix)+len(name) <= maxLineBytes {
		return prefix + name
	}
	note := fmt.Sprintf("…(%s truncated; it is %d bytes)", noun, len(name))
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

// SearchOptions carries the host's caps for one search. A non-positive cap falls
// back to the package default, so a plugin can never run an unbounded search
// just because a host sent no cap.
type SearchOptions struct {
	// MaxMatches is the largest number of matching lines one search renders.
	MaxMatches int
	// MaxLineBytes is the largest length of one rendered line, the path and
	// line number prefix included.
	MaxLineBytes int
	// MaxFiles is the largest number of files one search reads.
	MaxFiles int
	// MaxFileBytes is the largest file one search reads at all. A larger file is
	// skipped and counted rather than read in part: a search that read only the
	// first bytes of a file would answer a question about the whole file with a
	// statement about its beginning.
	MaxFileBytes int
	// TrimIndent renders each matching line without its leading whitespace. Both
	// renderings state the same path, line number and line text; the choice is
	// what makes a candidate replacement observable in the tool result.
	TrimIndent bool
}

// Search renders a literal search for query under path. path is an absolute
// path the host already validated against the read root: no path is interpreted
// here, and nothing outside path is ever entered.
//
// The match is literal — strings.Contains against one line at a time — and that
// is deliberate. A regular expression would decide the cost of the call from the
// model's own query, so a question the tool cannot answer ("no such text") could
// become a pattern that backtracks until the call times out and reports a plugin
// failure instead of an empty result; and it would silently read a plain query as
// a pattern, so looking for `docs/architecture.md` would also match
// `docsXarchitecture.md`. A literal can do neither. A model that wants a pattern
// can search for its literals one at a time.
//
// A directory is walked depth-first in path order, and the walk cannot leave the
// read root: the host resolved the starting path inside the root, no symbolic
// link is followed — a link is counted and skipped, never entered and never read,
// so a link that points outside the root cannot pull the search out of it — and a
// directory named `..` is never returned by a directory read. A file larger than
// MaxFileBytes is never opened, and content with a NUL byte is skipped the way
// Read refuses it: it has no lines to search.
//
// Every cap that was reached is stated in the result instead of cutting it
// silently, and a search that stopped says so without pretending to know what it
// missed: at MaxMatches or MaxFiles the walk ends, and the result says the
// remaining paths were not searched, because a search that stopped cannot count
// the matches it did not find.
func Search(path, query string, opts SearchOptions) (string, error) {
	if err := ValidateQuery(query); err != nil {
		return "", err
	}
	if opts.MaxMatches <= 0 {
		opts.MaxMatches = DefaultSearchMatches
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = DefaultSearchLineBytes
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultSearchFiles
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultSearchFileBytes
	}
	start, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotSearchable, pathError(err))
	}
	s := &searcher{query: query, opts: opts, start: path}
	switch {
	case start.Mode()&fs.ModeSymlink != 0:
		// A search never follows a symbolic link, including as its own starting
		// point; the host resolves the path it validated, so reaching here means
		// the caller handed over a link rather than a resolved path.
		return "", fmt.Errorf("%w: it is a symbolic link", ErrNotSearchable)
	case start.IsDir():
		if err := s.walk(path); err != nil {
			return "", fmt.Errorf("%w: %s", ErrNotSearchable, pathError(err))
		}
	case start.Mode().IsRegular():
		s.consider(path, start.Size())
	default:
		return "", fmt.Errorf("%w: %s", ErrNotSearchable, filepath.Base(path))
	}
	return s.render(), nil
}

// searcher is the state of one search: the query, the caps, and everything the
// result has to state about what was looked at and what was left out.
type searcher struct {
	query string
	opts  SearchOptions
	start string

	matches []string

	// The counters below are why a result can state its own scope: how many
	// files were searched, and what was passed over and why.
	files      int
	binary     int
	oversize   int
	links      int
	others     int
	unreadable int

	stoppedAtMatches bool
	stoppedAtFiles   bool
}

// walk searches every file below dir, in the order the directories return their
// entries, and stops at the first cap that ends the walk.
func (s *searcher) walk(dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			s.unreadable++
			if entry != nil && entry.IsDir() {
				// The directory cannot be read; its entries are counted as an
				// unread path so the result states that the walk was incomplete.
				return fs.SkipDir
			}
			if path == dir {
				return err
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			s.links++
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			s.unreadable++
			return nil
		}
		if !info.Mode().IsRegular() {
			s.others++
			return nil
		}
		if s.consider(path, info.Size()) {
			return fs.SkipAll
		}
		return nil
	})
}

// consider searches one file, or counts why it was not searched. It reports
// whether the walk must stop: the file cap ends the walk, and so does a match
// that reached the match cap.
func (s *searcher) consider(path string, size int64) (stop bool) {
	if size > int64(s.opts.MaxFileBytes) {
		s.oversize++
		return false
	}
	if s.files == s.opts.MaxFiles {
		s.stoppedAtFiles = true
		return true
	}
	// The ceiling is checked against the bytes actually read as well, so a file
	// that grows after the check is still skipped instead of searched in part.
	text, reason, err := searchableText(path, s.opts.MaxFileBytes)
	if err != nil {
		s.unreadable++
		return false
	}
	switch reason {
	case reasonBinary:
		s.binary++
		return false
	case reasonOversize:
		s.oversize++
		return false
	}
	s.files++
	return s.collect(path, text)
}

// collect renders the matching lines of one file, in line order, and reports
// whether the match cap ended the search.
func (s *searcher) collect(path, text string) (stop bool) {
	name, err := filepath.Rel(s.start, path)
	if err != nil || name == "." {
		name = filepath.Base(path)
	}
	for i, line := range strings.Split(text, "\n") {
		// A line terminator is not part of the text a match is decided on, so a
		// CRLF file renders like an LF one.
		line = strings.TrimSuffix(line, "\r")
		if !strings.Contains(line, s.query) {
			continue
		}
		if len(s.matches) == s.opts.MaxMatches {
			s.stoppedAtMatches = true
			return true
		}
		rendered := line
		if s.opts.TrimIndent {
			rendered = strings.TrimLeft(line, " \t")
		}
		s.matches = append(s.matches, fitLine(fmt.Sprintf("%s:%d: ", name, i+1), rendered, s.opts.MaxLineBytes, "line"))
	}
	return false
}

// render states what the search found and what it cost: how many matches in how
// many files, followed by every cap it reached and everything it did not search.
// A result that hid either half would describe a smaller question than the one
// that was asked.
func (s *searcher) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s for %q in %s", matchCount(len(s.matches)), s.query, plural(s.files, "file"))
	if notes := s.notes(); len(notes) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(notes, "; "))
	}
	if len(s.matches) == 0 {
		b.WriteString(".\n")
		return b.String()
	}
	b.WriteString(":\n")
	for _, match := range s.matches {
		b.WriteString(match)
		b.WriteString("\n")
	}
	return b.String()
}

// notes lists what the result has to say besides the matches, in a fixed order:
// first what was passed over, which is what the file count means, and then the
// cap that ended the walk.
func (s *searcher) notes() []string {
	notes := make([]string, 0, 6)
	if s.binary > 0 {
		notes = append(notes, fmt.Sprintf("%s skipped as binary", plural(s.binary, "file")))
	}
	if s.oversize > 0 {
		notes = append(notes, fmt.Sprintf("%s larger than the %d-byte per-file limit", plural(s.oversize, "file"), s.opts.MaxFileBytes))
	}
	if s.links > 0 {
		notes = append(notes, fmt.Sprintf("%s not followed", plural(s.links, "symbolic link")))
	}
	if s.others > 0 {
		notes = append(notes, fmt.Sprintf("%s not a regular file", plural(s.others, "path")))
	}
	if s.unreadable > 0 {
		notes = append(notes, fmt.Sprintf("%s could not be read", plural(s.unreadable, "path")))
	}
	if s.stoppedAtMatches {
		notes = append(notes, fmt.Sprintf("the search stopped at %s; the remaining paths were not searched", plural(s.opts.MaxMatches, "match")))
	}
	if s.stoppedAtFiles {
		notes = append(notes, fmt.Sprintf("the search stopped after %s; the remaining paths were not searched", plural(s.opts.MaxFiles, "file")))
	}
	return notes
}

// The reasons a file could not be searched. They are not errors: each one is
// counted and stated, and the search goes on.
const (
	reasonText     = "text"
	reasonBinary   = "binary"
	reasonOversize = "oversize"
)

// searchableText returns the text of path if it can be searched, or the reason
// it cannot. The criteria are the ones Read states — at most maxBytes bytes, and
// no NUL byte anywhere in the bytes actually read — so a file the model could not
// read is not one it can search either. One byte past the cap is read, so a file
// that is too large is recognised as too large instead of being searched in part.
func searchableText(path string, maxBytes int) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > maxBytes {
		return "", reasonOversize, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", reasonBinary, nil
	}
	return string(data), reasonText, nil
}

// matchCount renders a match count so the header never says "0 matches for"
// where it can say that nothing was found.
func matchCount(n int) string {
	if n == 0 {
		return "no matches"
	}
	return plural(n, "match")
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
	if strings.HasSuffix(noun, "ch") || strings.HasSuffix(noun, "sh") || strings.HasSuffix(noun, "s") || strings.HasSuffix(noun, "x") {
		return fmt.Sprintf("%d %ses", n, noun)
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
