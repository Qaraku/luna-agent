// Package memory keeps Luna's durable facts on disk as an append-only JSONL
// file: one fact per line, one JSON object per fact, under a configurable path.
//
// The record shape is frozen by the S3a spec (`type` / `text` / `at` /
// `source_session`). As with the session store, the shape has no field for
// plugin identity (generation, version, process id) and no field for a
// credential, so neither can travel through memory into a model context.
//
// Memory is core state, not a replaceable extension: the model can add a fact
// through the host-native `luna_remember` tool, and there is no read or delete
// path — reading happens only by system injection.
//
// Three properties carry the design:
//
//   - append only. A write that fits the caps appends one line with one Write
//     call, so a crash can lose the unterminated fragment at the tail and
//     nothing else.
//   - one writer. Both the read side and the write side take the store mutex,
//     so concurrent runs cannot interleave halves of a line and a
//     read-modify-write cannot lose a fact.
//   - bounded. Two explicit caps — a fact count and a byte size — are enforced
//     by dropping the oldest facts, which is the one case that rewrites the
//     file (atomically, through a temporary file and a rename).
//
// A tolerant read drops an unterminated trailing fragment (and repairs the file
// on the next write) instead of failing, while a malformed record anywhere else
// is a loud error, never a silent gap.
package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// TypeFact is the frozen record type of one stored fact.
const TypeFact = "fact"

const (
	// MaxFactChars is the character (rune) cap of one fact's text. It is a
	// model-facing input bound: luna_remember refuses anything longer, and the
	// store refuses it again rather than trusting a caller.
	MaxFactChars = 500
	// MaxFacts is the count cap of the store. Above it the oldest facts go.
	MaxFacts = 200
	// MaxBytes is the byte cap of the store, measured as the total size of the
	// complete lines in the file. Above it the oldest facts go.
	MaxBytes = 32 * 1024

	dirPerm  = 0o700
	filePerm = 0o600
)

var (
	// ErrNoFile reports a store opened without a memory file path.
	ErrNoFile = errors.New("memory: a memory file path is required")
	// ErrEmptyFact reports fact text that is empty or only whitespace. An
	// empty fact would carry no information, so it is refused rather than
	// stored as a blank line.
	ErrEmptyFact = errors.New("memory: fact text is required")
	// ErrFactTooLong reports fact text above MaxFactChars characters.
	ErrFactTooLong = fmt.Errorf("memory: fact text is longer than %d characters", MaxFactChars)
	// ErrCorrupt reports a memory file whose complete records cannot be
	// trusted. It never carries a host path.
	ErrCorrupt = errors.New("corrupt memory file")
)

// Fact is one stored fact, frozen by the S3a spec.
type Fact struct {
	Type          string    `json:"type"`
	Text          string    `json:"text"`
	At            time.Time `json:"at"`
	SourceSession string    `json:"source_session"`
}

// Store is an append-only, bounded JSONL fact file.
type Store struct {
	path string

	// mu serializes every read and every write. It is the single writer side:
	// concurrent runs append whole lines, never interleaved halves, and a
	// read-modify-write (which is what the caps need) cannot lose a fact.
	mu sync.Mutex
}

// Open prepares path as a memory file, creating its directory when missing. It
// does not read or create the file: an absent file is an empty memory.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrNoFile
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("memory: create memory directory: %w", err)
	}
	return &Store{path: path}, nil
}

// Facts returns every stored fact in file order, oldest first. An unterminated
// trailing fragment is dropped; a malformed complete record is an error.
func (s *Store) Facts() ([]Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	facts, _, err := readFacts(s.path)
	return facts, err
}

// Remember validates one fact, appends it while holding the store lock, and
// enforces both caps by dropping the oldest facts. The fact that was just
// accepted is never the one dropped.
func (s *Store) Remember(sourceSession, text string, at time.Time) (Fact, error) {
	if err := ValidateText(text); err != nil {
		return Fact{}, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	fact := Fact{Type: TypeFact, Text: text, At: at, SourceSession: sourceSession}

	s.mu.Lock()
	defer s.mu.Unlock()
	facts, truncated, err := readFacts(s.path)
	if err != nil {
		return Fact{}, err
	}
	kept, dropped, err := capFacts(append(facts, fact))
	if err != nil {
		return Fact{}, err
	}
	// The common case stays a pure append. A dropped fact, or a torn fragment
	// to repair, needs the whole file rewritten — atomically, so a crash can
	// never leave a half-written memory behind.
	if dropped == 0 && !truncated {
		if err := appendLine(s.path, fact); err != nil {
			return Fact{}, err
		}
		return fact, nil
	}
	if err := writeFacts(s.path, kept); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

// ValidateText bounds one fact's text. A caller may rely on the store doing it
// again, but the cap is stated once, here, so the tool and the store cannot
// drift apart.
func ValidateText(text string) error {
	if strings.TrimSpace(text) == "" {
		return ErrEmptyFact
	}
	if utf8.RuneCountInString(text) > MaxFactChars {
		return ErrFactTooLong
	}
	return nil
}

// readFacts decodes the file. A missing file is an empty memory rather than a
// failure, because the first run of a fresh checkout has no facts yet.
func readFacts(path string) ([]Fact, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("memory: read memory file: %w", err)
	}
	return parseFacts(data)
}

// parseFacts splits the file into facts. Every complete line must decode: only
// an unterminated trailing fragment is tolerated and reported, because a single
// Write call with a trailing newline is what a fact is. Errors name a line
// number and never a host path, so they can reach the model.
func parseFacts(data []byte) ([]Fact, bool, error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	parts := strings.Split(string(data), "\n")
	trailing := parts[len(parts)-1]
	parts = parts[:len(parts)-1]
	truncated := trailing != ""
	facts := make([]Fact, 0, len(parts))
	for i, line := range parts {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var fact Fact
		if err := json.Unmarshal([]byte(line), &fact); err != nil {
			return nil, truncated, fmt.Errorf("%w: line %d: %v", ErrCorrupt, i+1, err)
		}
		if fact.Type != TypeFact {
			return nil, truncated, fmt.Errorf("%w: line %d: unknown record type %q", ErrCorrupt, i+1, fact.Type)
		}
		facts = append(facts, fact)
	}
	return facts, truncated, nil
}

// capFacts drops the oldest facts until both caps hold, and reports how many
// went. The newest fact is always kept, even in the pathological case where it
// alone would exceed the byte cap: a fact the caller was just told was stored
// must not vanish in the same call.
func capFacts(facts []Fact) ([]Fact, int, error) {
	total := 0
	for _, fact := range facts {
		line, err := encodeLine(fact)
		if err != nil {
			return nil, 0, err
		}
		total += len(line)
	}
	dropped := 0
	for len(facts) > 1 && (len(facts) > MaxFacts || total > MaxBytes) {
		line, err := encodeLine(facts[0])
		if err != nil {
			return nil, 0, err
		}
		total -= len(line)
		facts = facts[1:]
		dropped++
	}
	return facts, dropped, nil
}

// appendLine writes one fact as one complete line. The file is opened read-write
// and appended to, so the write never truncates or rewrites a stored fact.
func appendLine(path string, fact Fact) error {
	line, err := encodeLine(fact)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, filePerm)
	if err != nil {
		return fmt.Errorf("memory: open memory file: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("memory: append fact: %w", err)
	}
	return nil
}

// writeFacts replaces the file with exactly these facts, oldest first. The
// content lands in a temporary file in the same directory and is renamed into
// place, so a reader sees either the old complete set or the new complete set.
func writeFacts(path string, facts []Fact) error {
	data := make([]byte, 0, len(facts)*64)
	for _, fact := range facts {
		line, err := encodeLine(fact)
		if err != nil {
			return err
		}
		data = append(data, line...)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "memory-*.tmp")
	if err != nil {
		return fmt.Errorf("memory: create temporary memory file: %w", err)
	}
	name := temp.Name()
	// A successful rename removes the temporary name; this only cleans up a
	// failure path.
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("memory: write temporary memory file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("memory: close temporary memory file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("memory: replace memory file: %w", err)
	}
	return nil
}

// encodeLine renders one fact as a newline-terminated line, so a single Write is
// one complete record even if the process dies immediately afterwards.
func encodeLine(fact Fact) ([]byte, error) {
	data, err := json.Marshal(fact)
	if err != nil {
		return nil, fmt.Errorf("memory: encode fact: %w", err)
	}
	return append(data, '\n'), nil
}
