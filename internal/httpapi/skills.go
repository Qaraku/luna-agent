package httpapi

import (
	"fmt"
	"net/http"
	"strings"
)

// skillsStatePrefix is the interface's namespace for turning one skill off or
// on. It is not the same thing as /api/plugins/{id}/enable|disable: that
// changes a capability's lifecycle state, while this changes the user's own
// preference about one of the skills the Skills capability found. The
// capability stays in service either way.
const skillsStatePrefix = "/api/skills/"

// SkillRef is one skill as the catalog reports it. It is the shape this layer
// knows about the capability, so the capability itself needs no wire types and
// this layer needs no skill logic.
type SkillRef struct {
	Name           string
	Description    string
	Scope          string
	Enabled        bool
	DisabledReason string
}

// SkillCatalog is the Skills capability as this layer uses it: which skills
// exist, whether each is in service, and how to change one. The composition
// root supplies it, because turning a skill off is both a preference to store
// and a change to what the next run reads, and those two must happen together.
type SkillCatalog interface {
	// Skills lists every discovered skill, in discovery order.
	Skills() []SkillRef
	// SetState turns one skill off or on and returns its new state. found is
	// false when no skill by that name was discovered, and then nothing was
	// written. An error means the new state could not be recorded, and then
	// nothing changed either.
	SetState(name string, enabled bool) (SkillRef, bool, error)
}

// WithSkills supplies the skill catalog. A server built without one lists no
// skills and refuses to change any, rather than failing to start.
func WithSkills(catalog SkillCatalog) Option { return func(s *Server) { s.skills = catalog } }

// skillView is one skill as the browser reads it. `disabled_reason` is empty
// for a skill that is on, so a client tells "on" apart from "off, for this
// reason" by one field.
type skillView struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Scope          string `json:"scope"`
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason"`
}

type skillsResponse struct {
	Skills []skillView `json:"skills"`
}

// skillDescriptionChars is how much of a description the interface shows in a
// list. A discovered description may be up to 1024 characters — more than a row
// can hold — so the cut is stated in the text rather than left silent, for the
// same reason the manifest states its own: a client that cannot tell a cut
// description from a short one will quietly show less than there is.
const skillDescriptionChars = 200

// describe is the wire form of a description: trimmed, and cut with the loss
// written out.
func describe(description string) string {
	trimmed := strings.TrimSpace(description)
	runes := []rune(trimmed)
	if len(runes) <= skillDescriptionChars {
		return trimmed
	}
	return string(runes[:skillDescriptionChars]) +
		fmt.Sprintf("… (this description was cut at %d characters; it is %d characters long)", skillDescriptionChars, len(runes))
}

// skillRefView renders the catalog's answer for the browser.
func skillRefView(ref SkillRef) skillView {
	return skillView{
		Name:           ref.Name,
		Description:    describe(ref.Description),
		Scope:          ref.Scope,
		Enabled:        ref.Enabled,
		DisabledReason: ref.DisabledReason,
	}
}

// sendSkills lists every discovered skill and its state. An empty list is an
// empty list rather than a missing field, so a browser never has to tell "no
// skills" apart from "nothing was sent".
//
// Skills that were rejected at discovery are not here at all: they are reported
// once, at start-up, to the person who put them on disk. What this endpoint
// answers is which skills are usable, and a rejected one is not.
func (s *Server) sendSkills(w http.ResponseWriter) {
	views := make([]skillView, 0)
	if s.skills != nil {
		for _, ref := range s.skills.Skills() {
			views = append(views, skillRefView(ref))
		}
	}
	send(w, 200, skillsResponse{Skills: views})
}

// setSkillState turns one skill off or on and answers with its new state.
//
// Where the preference is stored belongs to the composition root behind the
// catalog, and so does keeping the stored preference and the running capability
// in step. This layer owns the request shape, the status codes and the lifecycle
// event.
//
// Unlike POST /api/plugins/{id}/enable|disable there is no conflict to report
// here: the disabled skills are a set of names, not a lifecycle state machine,
// so asking for the state a skill is already in succeeds and leaves the file
// unchanged in meaning. An unknown name is 404 and writes nothing.
func (s *Server) setSkillState(w http.ResponseWriter, name, action string) {
	if s.skills == nil {
		fail(w, 500, fmt.Errorf("no skill catalog is configured"))
		return
	}
	ref, found, err := s.skills.SetState(name, action == "enable")
	if err != nil {
		fail(w, 500, err)
		return
	}
	if !found {
		fail(w, 404, fmt.Errorf("unknown skill %q", name))
		return
	}
	state := "disabled"
	if ref.Enabled {
		state = "enabled"
	}
	s.addEvent("skill_"+state, fmt.Sprintf("skill %q is now %s", ref.Name, state))
	send(w, 200, skillRefView(ref))
}

// skillStatePath splits /api/skills/{name}/{enable|disable}. The name is handed
// on unread: whether a skill by that name exists is the capability's answer, and
// parsing a path is not a way to know it.
func skillStatePath(path string) (name, action string, ok bool) {
	rest, found := strings.CutPrefix(path, skillsStatePrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" {
		return "", "", false
	}
	if parts[1] != "enable" && parts[1] != "disable" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
