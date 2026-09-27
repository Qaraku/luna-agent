package fileread

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The find caps. One find renders at most DefaultFindPaths matching paths,
// examines at most DefaultFindEntries entries and renders one path in at most
// DefaultFindLineBytes bytes. Everything it did not look at is stated in the
// result, so the caps bound the answer without making it a partial claim.
//
// The entry cap is deliberately far larger than the search's file cap: a find
// reads no file content, so the cost of one entry is the directory read that
// produced it rather than a read of the file itself. It is still a cap, because
// a walk of a tree with a dependency directory in it must end and say where it
// ended instead of running on.
const (
	DefaultFindPaths     = 100
	DefaultFindLineBytes = 200
	DefaultFindEntries   = 20000
)

// MaxPatternBytes is the longest name pattern one find accepts. A longer
// pattern is refused rather than cut, because a cut pattern would match names
// the model never asked about.
const MaxPatternBytes = 256

// Find refusals are sentinel errors, for the same reason the read, list and
// search refusals are: a caller and a test classify a refusal without matching
// on message text.
var (
	// ErrPatternEmpty reports an empty (or whitespace-only) pattern, which
	// would match nothing and say nothing.
	ErrPatternEmpty = errors.New("a name pattern is required")
	// ErrPatternInvalid reports a pattern the find cannot evaluate: one with a
	// NUL byte, one with a line break, one with a path separator, or one that
	// is not a valid glob.
	ErrPatternInvalid = errors.New("the name pattern is not valid")
	// ErrPatternTooLarge reports a pattern longer than MaxPatternBytes.
	ErrPatternTooLarge = errors.New("the name pattern is longer than the limit")
)

// ValidatePattern checks one name pattern before any work is done with it. It
// lives here, next to the find that consumes it, so the host can refuse a
// pattern on the same terms the plugin would — and so a malformed pattern is
// refused before a directory is read rather than answering with an empty result
// that looked like an answer.
//
// The pattern is a glob over one entry name, in path.Match's language: `*`
// matches any run of characters, `?` matches one character, `[...]` matches one
// character from a set or range, and a backslash escapes the next character.
// Nothing else is interpreted, so a dot is a dot — this is not a regular
// expression, and the reason it is not is the reason the search's query is not:
// a pattern language decides the cost of a call from the model's own input, and
// it would silently read a plain name as a pattern.
//
// A pattern may not contain a path separator. What a pattern matches is one
// entry name, and a pattern naming a path could never match anything, so it is
// refused with that reason instead of coming back empty.
func ValidatePattern(pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return ErrPatternEmpty
	case len(pattern) > MaxPatternBytes:
		return fmt.Errorf("%w: %d bytes, over the %d-byte limit", ErrPatternTooLarge, len(pattern), MaxPatternBytes)
	case strings.ContainsRune(pattern, 0):
		return fmt.Errorf("%w: it contains a NUL byte", ErrPatternInvalid)
	case strings.ContainsAny(pattern, "\n\r"):
		return fmt.Errorf("%w: it contains a line break", ErrPatternInvalid)
	case strings.ContainsRune(pattern, '/'):
		return fmt.Errorf("%w: it contains a path separator, and a pattern matches one entry name rather than a path", ErrPatternInvalid)
	}
	// A pattern that does not parse is refused here, on the same terms the
	// find would apply if it reached it: an unevaluable pattern is not an
	// empty result, and the empty name is enough to reach the syntax check
	// because path.Match scans the whole pattern for one.
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("%w: %s", ErrPatternInvalid, err)
	}
	return nil
}

// FindOptions carries the host's caps for one name search. A non-positive cap
// falls back to the package default, so a plugin can never run an unbounded
// walk just because a host sent no cap.
type FindOptions struct {
	// MaxPaths is the largest number of matching paths one find renders.
	MaxPaths int
	// MaxLineBytes is the largest length of one rendered line, the kind and
	// size prefix included.
	MaxLineBytes int
	// MaxEntries is the largest number of directory entries one find examines.
	MaxEntries int
	// ExactBytes renders every file size as an exact byte count instead of a
	// human-readable unit. It means here exactly what it means for a listing:
	// both renderings round the same measured number, so neither invents a
	// size, and the choice is what makes a candidate replacement observable in
	// the tool result.
	ExactBytes bool
}

// Find renders every entry at or below start whose name matches pattern. start
// is an absolute path the host already validated against the read root: no path
// is interpreted here, and nothing outside start is ever entered.
//
// The pattern is a glob over one entry name — see ValidatePattern — and it is
// anchored to the whole name rather than tested as a substring, so a pattern
// that names a file matches exactly that file and `*_test.go` matches every
// test file at every depth. That anchoring is the point of the tool: a name
// search answers "which files are called this" without the model having to
// guess whether its pattern was read as a prefix, a substring or an expression.
//
// What is rendered is the shape of the tree, not its content: one line per
// match, with the entry's kind, a size for regular files and the path relative
// to start, and a directory marked with a trailing slash so a match that is a
// directory cannot be mistaken for a file. Every entry the walk sees is
// examined, whatever its kind, and no entry is hidden.
//
// A directory is walked depth-first in path order, and the walk cannot leave
// start: the host resolved the starting path inside a root, no symbolic link is
// followed — a link is reported by its own name, because that name is a real
// entry, but never entered and never resolved, so a link that points outside
// the root cannot pull the walk out of it — and a directory named `..` is never
// returned by a directory read. A directory that cannot be read is counted, so
// a result that could not look somewhere says so.
//
// Every cap that was reached is stated in the result instead of cutting it
// silently, and a find that stopped says so without pretending to know what it
// missed: at MaxPaths or MaxEntries the walk ends, and the result says the
// remaining entries were not examined, because a find that stopped cannot
// report the matches it did not look for.
func Find(start, pattern string, opts FindOptions) (string, error) {
	if err := ValidatePattern(pattern); err != nil {
		return "", err
	}
	if opts.MaxPaths <= 0 {
		opts.MaxPaths = DefaultFindPaths
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = DefaultFindLineBytes
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultFindEntries
	}
	info, err := os.Lstat(start)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotSearchable, pathError(err))
	}
	f := &finder{pattern: pattern, opts: opts, start: start}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		// A find never follows a symbolic link, including as its own starting
		// point; the host resolves the path it validated, so reaching here
		// means the caller handed over a link rather than a resolved path.
		return "", fmt.Errorf("%w: it is a symbolic link", ErrNotSearchable)
	case info.IsDir():
		if err := f.walk(start); err != nil {
			return "", fmt.Errorf("%w: %s", ErrNotSearchable, pathError(err))
		}
	case info.Mode().IsRegular():
		f.consider(start, fileEntry{info: info})
	default:
		return "", fmt.Errorf("%w: %s", ErrNotSearchable, filepath.Base(start))
	}
	if f.patternErr != nil {
		return "", fmt.Errorf("%w: %s", ErrPatternInvalid, f.patternErr)
	}
	return f.render(), nil
}

// fileEntry adapts the metadata of the path a find starts at to the fs.DirEntry
// an entry is examined through, so a find of one file renders its line with the
// code the walk uses rather than with a second implementation.
type fileEntry struct{ info os.FileInfo }

func (e fileEntry) Name() string               { return e.info.Name() }
func (e fileEntry) IsDir() bool                { return e.info.IsDir() }
func (e fileEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e fileEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// finder is the state of one find: the pattern, the caps, and everything the
// result has to state about what was looked at and what was passed over.
type finder struct {
	pattern string
	opts    FindOptions
	start   string

	matches []string

	// The counters below are why a result can state its own scope.
	entries    int
	links      int
	unreadable int

	// patternErr records a pattern path.Match refused during the walk. It is
	// unreachable while ValidatePattern guards Find, and it is here so that
	// being unreachable is not the same as being unhandled: a pattern that
	// could not be evaluated would otherwise match nothing, and matching
	// nothing is an answer.
	patternErr error

	stoppedAtPaths   bool
	stoppedAtEntries bool
}

// walk examines every entry below dir, in the order the directories return
// their entries, and stops at the first cap that ends the walk.
func (f *finder) walk(dir string) error {
	return filepath.WalkDir(dir, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			f.unreadable++
			if entry != nil && entry.IsDir() {
				// The directory cannot be read; its entries are counted as an
				// unread path so the result states that the walk was
				// incomplete.
				return fs.SkipDir
			}
			if current == dir {
				return err
			}
			return nil
		}
		if current == dir {
			// The starting directory is where the walk starts, not an entry of
			// it: what a pattern matches is what is inside.
			return nil
		}
		if f.consider(current, entry) {
			return fs.SkipAll
		}
		return nil
	})
}

// consider examines one entry and reports whether the walk must stop.
func (f *finder) consider(current string, entry fs.DirEntry) (stop bool) {
	if f.entries == f.opts.MaxEntries {
		f.stoppedAtEntries = true
		return true
	}
	f.entries++
	if entry.Type()&fs.ModeSymlink != 0 {
		f.links++
	}
	matched, matchErr := path.Match(f.pattern, entry.Name())
	if matchErr != nil {
		f.patternErr = matchErr
		return true
	}
	if !matched {
		return false
	}
	if len(f.matches) == f.opts.MaxPaths {
		f.stoppedAtPaths = true
		return true
	}
	f.matches = append(f.matches, f.renderMatch(entry, current))
	return false
}

// renderMatch states one match: what the entry is, its size when it has one,
// and its path relative to the starting path. The rendering is the listing's,
// with the name replaced by the relative path, so a find and a listing describe
// the same entry the same way.
func (f *finder) renderMatch(entry fs.DirEntry, current string) string {
	name, err := filepath.Rel(f.start, current)
	if err != nil || name == "." {
		name = filepath.Base(current)
	}
	item := classify(entry, f.opts.ExactBytes)
	if entry.IsDir() {
		name += "/"
	}
	return fitLine(fmt.Sprintf("%-4s %9s  ", item.kind, item.size), name, f.opts.MaxLineBytes, "path")
}

// render states what the find found and what it cost: how many matching paths
// among how many entries examined, followed by everything it passed over and
// every cap it reached. A result that hid either half would describe a smaller
// question than the one that was asked.
func (f *finder) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q among %s examined", pathCount(len(f.matches)), f.pattern, plural(f.entries, "entry"))
	if notes := f.notes(); len(notes) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(notes, "; "))
	}
	if len(f.matches) == 0 {
		b.WriteString(".\n")
		return b.String()
	}
	b.WriteString(":\n")
	for _, match := range f.matches {
		b.WriteString(match)
		b.WriteString("\n")
	}
	return b.String()
}

// notes lists what the result has to say besides the matches, in a fixed order:
// first what was passed over, and then the cap that ended the walk.
func (f *finder) notes() []string {
	notes := make([]string, 0, 4)
	if f.links > 0 {
		notes = append(notes, fmt.Sprintf("%s not entered", plural(f.links, "symbolic link")))
	}
	if f.unreadable > 0 {
		notes = append(notes, fmt.Sprintf("%s could not be read", plural(f.unreadable, "path")))
	}
	if f.stoppedAtPaths {
		notes = append(notes, fmt.Sprintf("the find stopped at %s; the remaining entries were not examined", plural(f.opts.MaxPaths, "path")))
	}
	if f.stoppedAtEntries {
		notes = append(notes, fmt.Sprintf("the find stopped after %s; the remaining entries were not examined", plural(f.opts.MaxEntries, "entry")))
	}
	return notes
}

// pathCount renders a count of matching paths so the header never says "0
// paths match" where it can say that nothing matched.
func pathCount(n int) string {
	switch {
	case n == 0:
		return "no path matches"
	case n == 1:
		return "1 path matches"
	default:
		return fmt.Sprintf("%d paths match", n)
	}
}
