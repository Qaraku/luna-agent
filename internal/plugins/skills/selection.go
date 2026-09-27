package skills

import (
	"sync"

	"github.com/Qaraku/luna-agent/internal/skills"
)

// selection is the capability's own answer to "which of the discovered skills
// are in service". The manifest and the tool both read it and the interface
// changes it, so it is guarded by a mutex: a run and an interface request can be
// in flight at the same time.
//
// A nil *selection is the empty one — nothing turned off. That is what lets the
// tool be read in a test that never mentions settings, and it keeps "the user
// turned everything on" and "there is no setting behind this tool" the same
// answer, which they are.
type selection struct {
	mu       sync.RWMutex
	disabled map[string]bool
}

// newSelection builds a selection with the given names already turned off.
func newSelection(names ...string) *selection {
	sel := &selection{disabled: make(map[string]bool, len(names))}
	for _, name := range names {
		sel.set(name, true)
	}
	return sel
}

// off reports whether a skill by this name has been turned off.
func (s *selection) off(name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.disabled[name]
}

// set turns one name off or on. It does not ask whether the name was
// discovered: the caller answers that against the found list, and a name turned
// off before its directory was removed stays in the settings file without being
// a skill here.
func (s *selection) set(name string, disabled bool) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if disabled {
		s.disabled[name] = true
		return
	}
	delete(s.disabled, name)
}

// on returns the skills that are in service, in discovery order. The manifest is
// built from exactly this list, so a skill the user turned off is not in it: the
// model never reads a name the tool would then refuse.
func (s *selection) on(found []skills.Skill) []skills.Skill {
	enabled := make([]skills.Skill, 0, len(found))
	for _, skill := range found {
		if !s.off(skill.Name) {
			enabled = append(enabled, skill)
		}
	}
	return enabled
}
