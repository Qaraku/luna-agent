// Package memory keeps Luna's durable facts on disk as an append-only JSONL
// file: one fact per line, one JSON object per fact, under a configurable path.
//
// The record shape is frozen by the S3a spec (`type` / `text` / `at` /
// `source_session`). As with the session store, the shape has no field for
// plugin identity (generation, version, process id) and no field for a
// credential, so neither can travel through memory into a model context.
//
// Memory is core state, not a replaceable extension: the model can add a fact
// through the host-native `luna_remember` tool, and it still has no read, list
// or retract path. Reading happens only by system injection, and the user can
// retract a stored fact from the browser — both go through this store, so the
// model-visible surface never widens.
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
//   - retractable without a rewrite. A retraction is one appended record naming
//     the fact it removes, and a read folds it out of the effective set. The
//     byte cap counts every complete line, so retractions cannot grow the file
//     without bound, and a rewrite compacts them away together with the facts
//     they removed.
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

// TypeRetract is the record type of one retraction: a fact the user removed,
// written as an appended record rather than by rewriting the file.
const TypeRetract = "retract"

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
	// ErrUnknownFact reports a retraction that names no fact in effect: the
	// fact was already retracted, or the file never held it. A retraction that
	// cannot name what it removes is refused rather than appended blindly.
	ErrUnknownFact = errors.New("memory: no stored fact matches that text and time")
)

// Fact is one stored fact, frozen by the S3a spec.
type Fact struct {
	Type          string    `json:"type"`
	Text          string    `json:"text"`
	At            time.Time `json:"at"`
	SourceSession string    `json:"source_session"`
}

// RetractRecord is the appended record of one retraction. It names its target by
// both the fact's timestamp and its text, so a retraction can only ever remove
// the fact it was made about.
type RetractRecord struct {
	Type       string    `json:"type"`
	At         time.Time `json:"at"`
	TargetAt   time.Time `json:"target_at"`
	TargetText string    `json:"target_text"`
}

// Retracted is a fact that is no longer in effect, together with when it was
// retracted. The fact itself is kept whole so the browser can show what was
// removed.
type Retracted struct {
	Fact        Fact      `json:"fact"`
	RetractedAt time.Time `json:"retracted_at"`
}

// Snapshot is the read model of the store: the facts still in effect, oldest
// first, and the facts that have been retracted, in retraction order.
type Snapshot struct {
	Facts     []Fact      `json:"facts"`
	Retracted []Retracted `json:"retracted"`
}

// record is one decoded line. Facts and retractions share the file, so one
// struct carries both shapes and the type field decides which one it is.
type record struct {
	Type          string    `json:"type"`
	Text          string    `json:"text,omitempty"`
	At            time.Time `json:"at"`
	SourceSession string    `json:"source_session,omitempty"`
	TargetAt      time.Time `json:"target_at,omitempty"`
	TargetText    string    `json:"target_text,omitempty"`
}

func factRecord(fact Fact) record {
	return record{Type: TypeFact, Text: fact.Text, At: fact.At, SourceSession: fact.SourceSession}
}

func retractRecord(at time.Time, target Fact) record {
	return record{Type: TypeRetract, At: at, TargetAt: target.At, TargetText: target.Text}
}

// factFor renders a fact record back into a Fact.
func (r record) factFor() Fact {
	return Fact{Type: TypeFact, Text: r.Text, At: r.At, SourceSession: r.SourceSession}
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

// Facts returns the facts in effect, in file order, oldest first: a retracted
// fact is not among them. An unterminated trailing fragment is dropped; a
// malformed complete record is an error.
func (s *Store) Facts() ([]Fact, error) {
	snapshot, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Facts, nil
}

// Snapshot returns the facts in effect and the facts that were retracted.
func (s *Store) Snapshot() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, _, err := readRecords(s.path)
	if err != nil {
		return Snapshot{}, err
	}
	facts, retracted := fold(records)
	return Snapshot{Facts: facts, Retracted: retracted}, nil
}

// Retract takes one fact out of the effective set by appending a retraction
// record; no stored line is rewritten. The fact is identified by its timestamp
// and its text together. A retraction that matches nothing in effect is
// ErrUnknownFact, and nothing is written.
func (s *Store) Retract(targetAt time.Time, targetText string) (Retracted, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, truncated, err := readRecords(s.path)
	if err != nil {
		return Retracted{}, err
	}
	facts, _ := fold(records)
	idx := indexOfFact(facts, targetAt, targetText)
	if idx < 0 {
		return Retracted{}, ErrUnknownFact
	}
	target := facts[idx]
	rec := retractRecord(time.Now(), target)
	if err := appendRecord(s.path, records, truncated, rec); err != nil {
		return Retracted{}, err
	}
	return Retracted{Fact: target, RetractedAt: rec.At}, nil
}

// Remember validates one fact, appends it under the store lock, and enforces
// both caps by dropping the oldest facts. The fact that was just accepted is
// never the one dropped.
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
	records, truncated, err := readRecords(s.path)
	if err != nil {
		return Fact{}, err
	}
	if err := appendRecord(s.path, records, truncated, factRecord(fact)); err != nil {
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

// readRecords decodes the file. A missing file is an empty memory rather than a
// failure, because the first run of a fresh checkout has no facts yet.
func readRecords(path string) ([]record, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("memory: read memory file: %w", err)
	}
	return parseRecords(data)
}

// parseRecords splits the file into records. Every complete line must decode:
// only an unterminated trailing fragment is tolerated and reported, because a
// single Write call with a trailing newline is what a record is. Errors name a
// line number and never a host path, so they can reach the model.
func parseRecords(data []byte) ([]record, bool, error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	parts := strings.Split(string(data), "\n")
	trailing := parts[len(parts)-1]
	parts = parts[:len(parts)-1]
	truncated := trailing != ""
	records := make([]record, 0, len(parts))
	for i, line := range parts {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, truncated, fmt.Errorf("%w: line %d: %v", ErrCorrupt, i+1, err)
		}
		switch rec.Type {
		case TypeFact, TypeRetract:
		default:
			return nil, truncated, fmt.Errorf("%w: line %d: unknown record type %q", ErrCorrupt, i+1, rec.Type)
		}
		records = append(records, rec)
	}
	return records, truncated, nil
}

// fold applies every retraction to the facts recorded before it and returns the
// facts still in effect, oldest first, plus what was removed. A retraction that
// names no fact in effect — a redundant record, or one whose target a rewrite
// already compacted away — is ignored rather than reported as corruption: it
// removes nothing, which is exactly what it asked for.
func fold(records []record) ([]Fact, []Retracted) {
	facts := make([]Fact, 0, len(records))
	retracted := make([]Retracted, 0)
	for _, rec := range records {
		switch rec.Type {
		case TypeFact:
			facts = append(facts, rec.factFor())
		case TypeRetract:
			idx := indexOfFact(facts, rec.TargetAt, rec.TargetText)
			if idx < 0 {
				continue
			}
			removed := facts[idx]
			facts = append(facts[:idx], facts[idx+1:]...)
			retracted = append(retracted, Retracted{Fact: removed, RetractedAt: rec.At})
		}
	}
	return facts, retracted
}

// indexOfFact finds the oldest fact matching both the timestamp and the text.
// Neither field is identity on its own: two facts can share a timestamp, and
// one text can legitimately be stored twice.
func indexOfFact(facts []Fact, at time.Time, text string) int {
	for i, fact := range facts {
		if fact.At.Equal(at) && fact.Text == text {
			return i
		}
	}
	return -1
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

// appendRecord adds one record to the file. The common case is a pure append,
// one Write call for one complete line. A write that would break a cap, or a
// file with a torn tail to repair, rewrites the file instead — with the folded
// effective facts, so a retraction and the fact it removed are compacted away
// together and a rewrite can never resurrect one.
func appendRecord(path string, records []record, truncated bool, rec record) error {
	line, err := encodeRecord(rec)
	if err != nil {
		return err
	}
	total, facts := 0, 0
	for _, existing := range records {
		encoded, err := encodeRecord(existing)
		if err != nil {
			return err
		}
		total += len(encoded)
		if existing.Type == TypeFact {
			facts++
		}
	}
	if rec.Type == TypeFact {
		facts++
	}
	// The byte cap counts every complete line, because a retraction is a line
	// too: without that, retracting in a loop would grow the file without bound.
	if !truncated && facts <= MaxFacts && total+len(line) <= MaxBytes {
		return appendLine(path, line)
	}
	kept, _, err := capFacts(foldFacts(append(records, rec)))
	if err != nil {
		return err
	}
	return writeFacts(path, kept)
}

// foldFacts is fold for callers that only need the facts left in effect.
func foldFacts(records []record) []Fact {
	facts, _ := fold(records)
	return facts
}

// appendLine writes one encoded record. The file is opened read-write and
// appended to, so the write never truncates or rewrites a stored record.
func appendLine(path string, line []byte) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, filePerm)
	if err != nil {
		return fmt.Errorf("memory: open memory file: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("memory: append record: %w", err)
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

// encodeLine renders one fact as a newline-terminated line. Fact lines keep the
// exact shape the S3a spec froze: type, text, at, source_session.
func encodeLine(fact Fact) ([]byte, error) {
	return encodeRecord(factRecord(fact))
}

// encodeRecord renders one record with exactly the fields its type has, so a
// retraction carries no fact-only field and a fact's shape never changes.
func encodeRecord(rec record) ([]byte, error) {
	var payload any
	switch rec.Type {
	case TypeFact:
		payload = Fact{Type: TypeFact, Text: rec.Text, At: rec.At, SourceSession: rec.SourceSession}
	case TypeRetract:
		payload = RetractRecord{Type: TypeRetract, At: rec.At, TargetAt: rec.TargetAt, TargetText: rec.TargetText}
	default:
		return nil, fmt.Errorf("memory: unknown record type %q", rec.Type)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("memory: encode record: %w", err)
	}
	return append(data, '\n'), nil
}
