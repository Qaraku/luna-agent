package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".runtime", "memory.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, path
}

func mustRead(t *testing.T, s *Store) []Fact {
	t.Helper()
	facts, err := s.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	return facts
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	if len(data) == 0 {
		return nil
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("the memory file does not end with a newline: %q", string(data))
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestOpenRequiresAPath(t *testing.T) {
	if _, err := Open("   "); !errors.Is(err, ErrNoFile) {
		t.Fatalf("err=%v, want ErrNoFile", err)
	}
}

func TestOpenCreatesTheMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "memory.jsonl")
	if _, err := Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() {
		t.Fatalf("the memory directory was not created: %v", err)
	}
}

// An absent file is an empty memory, never a failure: the first run of a fresh
// checkout has no facts yet.
func TestFactsOnAnAbsentFileAreEmpty(t *testing.T) {
	s, path := newStore(t)
	facts, err := s.Facts()
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("facts=%+v", facts)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading must not create the file: %v", err)
	}
}

func TestRememberThenFactsRoundTrips(t *testing.T) {
	s, path := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)

	stored, err := s.Remember("session-a", "prefers short answers", at)
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if stored.Type != TypeFact || stored.Text != "prefers short answers" || !stored.At.Equal(at) || stored.SourceSession != "session-a" {
		t.Fatalf("returned fact=%+v", stored)
	}
	if _, err := s.Remember("session-b", "writes Go", at.Add(time.Minute)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	facts := mustRead(t, s)
	if len(facts) != 2 {
		t.Fatalf("facts=%+v", facts)
	}
	wantText := []string{"prefers short answers", "writes Go"}
	wantSession := []string{"session-a", "session-b"}
	for i, fact := range facts {
		if fact.Type != TypeFact || fact.Text != wantText[i] || fact.SourceSession != wantSession[i] {
			t.Fatalf("facts[%d]=%+v", i, fact)
		}
	}
	if !facts[0].At.Equal(at) || !facts[1].At.Equal(at.Add(time.Minute)) {
		t.Fatalf("timestamps are out of order: %v", facts)
	}

	// One line per fact, each carrying the four frozen fields.
	written := lines(t, path)
	if len(written) != 2 {
		t.Fatalf("the file has %d lines, want one per fact", len(written))
	}
	for i, line := range written {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %d does not decode: %v", i+1, err)
		}
		keys := make([]string, 0, len(raw))
		for key := range raw {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "at,source_session,text,type" {
			t.Fatalf("line %d carries fields %v, want exactly type, text, at, source_session", i+1, keys)
		}
	}
}

// No plugin identity and no credential can travel through memory, because the
// frozen record shape has no field for either.
func TestTheRecordHasNoPluginIdentityOrCredentialField(t *testing.T) {
	encoded, err := json.Marshal(Fact{Type: TypeFact, Text: "x", At: time.Unix(0, 0), SourceSession: "s"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lowered := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"generation", "version", "plugin", "pid", "key", "token", "secret", "credential"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("the record shape mentions %q: %s", forbidden, encoded)
		}
	}
}

// A restarted process reopens the same file and reads the same facts back.
func TestFactsSurviveReopeningTheStore(t *testing.T) {
	s, path := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := s.Remember("session-a", fmt.Sprintf("fact-%d", i), at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("Remember: %v", err)
		}
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	facts := mustRead(t, reopened)
	if len(facts) != 3 {
		t.Fatalf("facts=%+v", facts)
	}
	for i, fact := range facts {
		if fact.Text != fmt.Sprintf("fact-%d", i) || fact.SourceSession != "session-a" {
			t.Fatalf("facts[%d]=%+v", i, fact)
		}
	}
}

// A crash can leave an unterminated fragment at the tail. It is dropped instead
// of making the whole memory unreadable, and the next write repairs the file.
func TestATornFinalLineIsDroppedAndRepairedOnTheNextWrite(t *testing.T) {
	s, path := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	if _, err := s.Remember("session-a", "kept fact", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := file.WriteString(`{"type":"fact","text":"tor`); err != nil {
		t.Fatalf("write fragment: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	facts := mustRead(t, s)
	if len(facts) != 1 || facts[0].Text != "kept fact" {
		t.Fatalf("a torn tail hid the complete facts: %+v", facts)
	}

	if _, err := s.Remember("session-b", "second fact", at.Add(time.Minute)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	facts = mustRead(t, reopened)
	if len(facts) != 2 || facts[0].Text != "kept fact" || facts[1].Text != "second fact" {
		t.Fatalf("facts=%+v", facts)
	}
	// The fragment is gone rather than merged into the new record.
	for _, line := range lines(t, path) {
		var fact Fact
		if err := json.Unmarshal([]byte(line), &fact); err != nil {
			t.Fatalf("a repaired line does not decode: %v", err)
		}
		if fact.Type != TypeFact {
			t.Fatalf("repaired line=%+v", fact)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "tor\"") || strings.Contains(string(data), `"tor`) {
		t.Fatalf("the torn fragment survived the repair: %s", data)
	}
}

func TestTheCountCapDropsTheOldestFactsFirst(t *testing.T) {
	s, _ := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	total := MaxFacts + 5
	for i := 0; i < total; i++ {
		if _, err := s.Remember("s", fmt.Sprintf("fact-%04d", i), at); err != nil {
			t.Fatalf("Remember: %v", err)
		}
	}
	facts := mustRead(t, s)
	if len(facts) != MaxFacts {
		t.Fatalf("kept %d facts, want the count cap %d", len(facts), MaxFacts)
	}
	if facts[0].Text != fmt.Sprintf("fact-%04d", total-MaxFacts) {
		t.Fatalf("dropping must start at the oldest fact: first=%q", facts[0].Text)
	}
	if facts[len(facts)-1].Text != fmt.Sprintf("fact-%04d", total-1) {
		t.Fatalf("the newest fact was dropped: last=%q", facts[len(facts)-1].Text)
	}
	for i := 1; i < len(facts); i++ {
		if facts[i-1].Text >= facts[i].Text {
			t.Fatalf("chronological order changed at %d: %q then %q", i, facts[i-1].Text, facts[i].Text)
		}
	}
}

func TestTheByteCapDropsTheOldestFactsFirst(t *testing.T) {
	s, path := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	texts := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		text := fmt.Sprintf("%03d-", i) + strings.Repeat("x", 396)
		texts = append(texts, text)
		if _, err := s.Remember("s", text, at); err != nil {
			t.Fatalf("Remember: %v", err)
		}
	}

	facts := mustRead(t, s)
	if len(facts) == 0 || len(facts) >= len(texts) {
		t.Fatalf("the byte cap dropped nothing: kept %d of %d", len(facts), len(texts))
	}
	if size := fileSize(t, path); size > MaxBytes {
		t.Fatalf("the memory file is %d bytes, above the %d-byte cap", size, MaxBytes)
	}
	// The kept facts are a contiguous newest suffix, in chronological order.
	offset := len(texts) - len(facts)
	for i, fact := range facts {
		if fact.Text != texts[offset+i] {
			t.Fatalf("facts[%d] is not the expected suffix element", i)
		}
	}
	// One more fact pushes another oldest fact out and keeps the file bounded.
	if _, err := s.Remember("s", "newest", at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	facts = mustRead(t, s)
	if size := fileSize(t, path); size > MaxBytes {
		t.Fatalf("the memory file is %d bytes, above the %d-byte cap", size, MaxBytes)
	}
	if facts[len(facts)-1].Text != "newest" {
		t.Fatalf("the newest fact is missing: %q", facts[len(facts)-1].Text)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return info.Size()
}

// A single accepted fact is never dropped in the same call that accepted it,
// even in the pathological case where it alone exceeds the byte cap.
func TestTheNewestFactIsAlwaysKept(t *testing.T) {
	huge := Fact{Type: TypeFact, Text: strings.Repeat("h", MaxBytes)}
	small := Fact{Type: TypeFact, Text: "small"}
	kept, dropped, err := capFacts([]Fact{huge, small})
	if err != nil {
		t.Fatalf("capFacts: %v", err)
	}
	if len(kept) != 1 || kept[0].Text != small.Text || dropped != 1 {
		t.Fatalf("kept=%d dropped=%d", len(kept), dropped)
	}
	kept, dropped, err = capFacts([]Fact{huge})
	if err != nil {
		t.Fatalf("capFacts: %v", err)
	}
	if len(kept) != 1 || dropped != 0 {
		t.Fatalf("kept=%d dropped=%d", len(kept), dropped)
	}
}

func TestRememberRejectsEmptyAndOversizedFacts(t *testing.T) {
	s, path := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		text string
		want error
	}{
		{"empty", "", ErrEmptyFact},
		{"whitespace only", "  \n\t ", ErrEmptyFact},
		{"one over the character cap", strings.Repeat("x", MaxFactChars+1), ErrFactTooLong},
		{"multi-byte over the character cap", strings.Repeat("語", MaxFactChars+1), ErrFactTooLong},
	}
	for _, tc := range cases {
		if _, err := s.Remember("s", tc.text, at); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v, want %v", tc.name, err, tc.want)
		}
	}
	if facts := mustRead(t, s); len(facts) != 0 {
		t.Fatalf("a refused fact reached the store: %+v", facts)
	}
	if _, err := os.Stat(path); err == nil {
		if len(lines(t, path)) != 0 {
			t.Fatal("a refused fact was written")
		}
	}
}

// The cap counts characters, not bytes: a 500-character fact of three-byte
// runes is accepted.
func TestTheCharacterCapIsOnCharactersNotBytes(t *testing.T) {
	s, _ := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	text := strings.Repeat("語", MaxFactChars)
	if _, err := s.Remember("s", text, at); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	facts := mustRead(t, s)
	if len(facts) != 1 || facts[0].Text != text {
		t.Fatalf("facts=%+v", facts)
	}
}

// A malformed complete line is a loud error, never a silent gap.
func TestAMalformedLineIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	valid := `{"type":"fact","text":"kept","at":"2026-09-23T10:00:00Z","source_session":"s"}` + "\n"
	for _, body := range []string{"not json\n", `{"type":"nonsense","text":"x"}` + "\n", `{"text":"missing the type"}` + "\n"} {
		if err := os.WriteFile(path, []byte(valid+body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := s.Facts(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("body %q: err=%v, want ErrCorrupt", body, err)
		}
	}
}

// Concurrent writers cannot interleave halves of a line: one mutex owns the
// read-modify-write, and no fact is lost.
func TestConcurrentWritersKeepWholeLines(t *testing.T) {
	s, path := newStore(t)
	at := time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC)
	const writers = 32
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Remember("s", fmt.Sprintf("fact-%02d", i), at); err != nil {
				t.Errorf("Remember: %v", err)
			}
		}(i)
	}
	wg.Wait()

	facts := mustRead(t, s)
	if len(facts) != writers {
		t.Fatalf("kept %d facts, want %d", len(facts), writers)
	}
	written := lines(t, path)
	if len(written) != writers {
		t.Fatalf("the file has %d lines, want %d", len(written), writers)
	}
	seen := map[string]bool{}
	for _, line := range written {
		var fact Fact
		if err := json.Unmarshal([]byte(line), &fact); err != nil {
			t.Fatalf("a line does not decode: %v", err)
		}
		seen[fact.Text] = true
	}
	if len(seen) != writers {
		t.Fatalf("distinct facts on disk=%d, want %d", len(seen), writers)
	}
}
