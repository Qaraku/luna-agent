package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Qaraku/luna-agent/internal/atomicfile"
)

const TypeChange = "change"
const MaxMemoryHistoryBytes = 1024 * 1024
const MaxMemoryChanges = 128

var ErrMemoryConflict = errors.New("memory changed, was withdrawn, or its reference is ambiguous; reload before changing it")
var ErrMemoryCapacity = errors.New("memory history capacity reached; retract unused active entries or export history before managing stored data")

type ChangeOrigin struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type MemoryChange struct {
	ID      string       `json:"id"`
	Action  string       `json:"action"`
	At      time.Time    `json:"at"`
	Before  Fact         `json:"before"`
	After   Fact         `json:"after"`
	FromRef string       `json:"from_ref,omitempty"`
	Origin  ChangeOrigin `json:"origin"`
}

func validChange(change *MemoryChange) bool {
	return change != nil && memoryIDPattern.MatchString(change.ID) && (change.Action == "correct" || change.Action == "restore") && validFactScope(change.Before) && validFactScope(change.After) && memoryIDPattern.MatchString(change.After.ID) && normalizedScope(change.Before.Scope) == normalizedScope(change.After.Scope) && change.Before.WorkspaceID == change.After.WorkspaceID && change.Before.family() == change.After.family() && (change.Origin.Kind == "model" || change.Origin.Kind == "user") && ValidateText(change.After.Text) == nil
}
func collectChanges(records []record) []MemoryChange {
	var out []MemoryChange
	for _, rec := range records {
		if rec.Change != nil {
			out = append(out, *rec.Change)
		}
	}
	return out
}
func modernRecord(rec record) bool {
	return rec.Type == TypeChange || (rec.Type == TypeFact && (rec.ID != "" || rec.Scope != ""))
}
func appendModernRecord(ctx context.Context, path string, records []record, truncated bool, rec record) (bool, error) {
	modern := modernRecord(rec)
	for _, existing := range records {
		modern = modern || modernRecord(existing)
	}
	if !modern {
		return false, nil
	}
	all := append(append([]record{}, records...), rec)
	active, _ := fold(all)
	changes := 0
	data := []byte{}
	for _, row := range all {
		if row.Type == TypeChange {
			changes++
		}
		line, err := encodeRecord(row)
		if err != nil {
			return true, err
		}
		data = append(data, line...)
	}
	if len(active) > MaxFacts {
		return true, fmt.Errorf("%w: active-note limit %d", ErrMemoryCapacity, MaxFacts)
	}
	if changes > MaxMemoryChanges {
		return true, fmt.Errorf("%w: correction-history limit %d", ErrMemoryCapacity, MaxMemoryChanges)
	}
	if len(data) > MaxMemoryHistoryBytes {
		return true, fmt.Errorf("%w: history-byte limit %d", ErrMemoryCapacity, MaxMemoryHistoryBytes)
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if truncated {
		return true, atomicfile.WriteFile(path, data, filePerm)
	}
	line, err := encodeRecord(rec)
	if err != nil {
		return true, err
	}
	return true, appendLine(path, line)
}
func (s *Store) Correct(ctx context.Context, ref, text string, origin ChangeOrigin) (MemoryChange, error) {
	if err := ValidateText(text); err != nil {
		return MemoryChange{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return MemoryChange{}, err
	}
	records, truncated, err := readRecords(s.path)
	if err != nil {
		return MemoryChange{}, err
	}
	active, _ := fold(records)
	idx := indexOfRef(active, ref)
	if idx < 0 {
		return MemoryChange{}, ErrMemoryConflict
	}
	return s.change(ctx, records, truncated, "correct", active[idx], text, "", origin)
}
func (s *Store) Restore(ctx context.Context, sourceRef, expectedRef string, origin ChangeOrigin) (MemoryChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return MemoryChange{}, err
	}
	records, truncated, err := readRecords(s.path)
	if err != nil {
		return MemoryChange{}, err
	}
	source, ok := findStoredRef(records, sourceRef)
	if !ok {
		return MemoryChange{}, ErrUnknownFact
	}
	active, _ := fold(records)
	var current *Fact
	for _, fact := range active {
		if fact.family() == source.family() {
			if current != nil {
				return MemoryChange{}, ErrMemoryConflict
			}
			copy := fact
			current = &copy
		}
	}
	before := source
	if expectedRef == "" {
		if current != nil {
			return MemoryChange{}, ErrMemoryConflict
		}
	} else {
		if current == nil || current.Ref() != expectedRef || current.Ref() == sourceRef {
			return MemoryChange{}, ErrMemoryConflict
		}
		before = *current
	}
	return s.change(ctx, records, truncated, "restore", before, source.Text, sourceRef, origin)
}
func findStoredRef(records []record, ref string) (Fact, bool) {
	for _, rec := range records {
		if rec.Type == TypeFact {
			fact := rec.factFor()
			if fact.Ref() == ref {
				return fact, true
			}
		}
		if rec.Change != nil {
			if rec.Change.Before.Ref() == ref {
				return rec.Change.Before, true
			}
			if rec.Change.After.Ref() == ref {
				return rec.Change.After, true
			}
		}
	}
	return Fact{}, false
}
func (s *Store) change(ctx context.Context, records []record, truncated bool, action string, before Fact, text, sourceRef string, origin ChangeOrigin) (MemoryChange, error) {
	if origin.Kind != "user" && origin.Kind != "model" || len(origin.Reason) > 2048 {
		return MemoryChange{}, fmt.Errorf("invalid memory change origin")
	}
	id, err := randomMemoryID()
	if err != nil {
		return MemoryChange{}, err
	}
	factID, err := randomMemoryID()
	if err != nil {
		return MemoryChange{}, err
	}
	now := time.Now()
	after := Fact{Type: TypeFact, ID: factID, Text: text, At: now, SourceSession: origin.SessionID, SourceRun: origin.RunID, Scope: normalizedScope(before.Scope), WorkspaceID: before.WorkspaceID, Origin: origin.Kind, Lineage: before.family()}
	change := MemoryChange{ID: id, Action: action, At: now, Before: before, After: after, FromRef: sourceRef, Origin: origin}
	if !validChange(&change) {
		return MemoryChange{}, ErrCorrupt
	}
	if err := ctx.Err(); err != nil {
		return MemoryChange{}, err
	}
	if err = appendRecordContext(ctx, s.path, records, truncated, record{Type: TypeChange, At: now, Change: &change}); err != nil {
		return MemoryChange{}, err
	}
	return change, nil
}
func (s *Store) RetractRef(ref string) (Retracted, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, truncated, err := readRecords(s.path)
	if err != nil {
		return Retracted{}, err
	}
	active, _ := fold(records)
	idx := indexOfRef(active, ref)
	if idx < 0 {
		return Retracted{}, ErrMemoryConflict
	}
	target := active[idx]
	now := time.Now()
	rec := retractRecord(now, target)
	rec.TargetRef = ref
	if err = appendRecord(s.path, records, truncated, rec); err != nil {
		return Retracted{}, err
	}
	return Retracted{Fact: target, RetractedAt: now}, nil
}
