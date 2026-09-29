package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const ScopeGlobal = "global"
const ScopeProject = "project"

var memoryIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var workspaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func normalizedScope(scope string) string {
	if scope == "" {
		return ScopeGlobal
	}
	return scope
}
func validFactScope(f Fact) bool {
	scope := normalizedScope(f.Scope)
	return (scope == ScopeGlobal && f.WorkspaceID == "") || (scope == ScopeProject && workspaceIDPattern.MatchString(f.WorkspaceID))
}
func (f Fact) Ref() string {
	if f.ID != "" {
		return "id:" + f.ID
	}
	data, _ := json.Marshal(f)
	sum := sha256.Sum256(data)
	return "legacy:" + hex.EncodeToString(sum[:])
}
func (f Fact) family() string {
	if f.Lineage != "" {
		return f.Lineage
	}
	return f.Ref()
}
func randomMemoryID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func filterFacts(facts []Fact, scope, workspace string) ([]Fact, error) {
	if scope == "" {
		scope = "current"
	}
	if scope != "current" && scope != ScopeGlobal && scope != ScopeProject && scope != "all" {
		return nil, fmt.Errorf("scope must be current, global, project or all")
	}
	if scope == ScopeProject && workspace == "" {
		return nil, fmt.Errorf("project scope requires a bound workspace")
	}
	out := []Fact{}
	for _, fact := range facts {
		if !validFactScope(fact) {
			return nil, ErrCorrupt
		}
		global := normalizedScope(fact.Scope) == ScopeGlobal
		match := scope == "all" || (scope == ScopeGlobal && global) || (scope == ScopeProject && !global && fact.WorkspaceID == workspace) || (scope == "current" && (global || workspace != "" && fact.WorkspaceID == workspace))
		if match {
			out = append(out, fact)
		}
	}
	return out, nil
}
func scopedFactText(fact Fact) string {
	if fact.Scope == "" {
		return fact.Text
	}
	if fact.Scope == ScopeGlobal {
		return "[global] " + fact.Text
	}
	return "[project " + fact.WorkspaceID + "] " + fact.Text
}
func (s *Store) RememberScoped(ctx context.Context, session, run, text, scope, workspace, origin string) (Fact, error) {
	if err := ValidateText(text); err != nil {
		return Fact{}, err
	}
	scope = normalizedScope(scope)
	fact := Fact{Type: TypeFact, Text: text, At: time.Now(), SourceSession: session, SourceRun: run, Scope: scope, WorkspaceID: workspace, Origin: origin}
	if !validFactScope(fact) || (origin != "model" && origin != "user") {
		return Fact{}, fmt.Errorf("invalid memory scope or origin")
	}
	id, err := randomMemoryID()
	if err != nil {
		return Fact{}, err
	}
	fact.ID = id
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Fact{}, err
	}
	records, truncated, err := readRecords(s.path)
	if err != nil {
		return Fact{}, err
	}
	if err := ctx.Err(); err != nil {
		return Fact{}, err
	}
	if err = appendRecordContext(ctx, s.path, records, truncated, factRecord(fact)); err != nil {
		return Fact{}, err
	}
	return fact, nil
}
func indexOfRef(facts []Fact, ref string) int {
	found := -1
	for i, fact := range facts {
		if fact.Ref() == ref {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}
