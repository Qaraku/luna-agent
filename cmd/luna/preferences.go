package main

import (
	"strings"
	"sync"

	"github.com/Qaraku/luna-agent/internal/httpapi"
	skillsplugin "github.com/Qaraku/luna-agent/internal/plugins/skills"
	"github.com/Qaraku/luna-agent/internal/settings"
)

// userPreferences is the settings file as this process uses it: the choices the
// user made in the product, and the one place they are written down.
//
// It lives in the composition root because a choice is two things at once — a
// preference to store and a change to what the next run reads — and this is the
// only layer that holds both. The HTTP layer is given what it needs to list and
// change one, and learns nothing about where any of it lives.
//
// One type owns the file on purpose: skills and capabilities are written to the
// same file, and two writers with two locks would drop whichever choice finished
// first. The mutex is what makes each change a whole rewrite of the choices as
// they stand, rather than a rewrite of the one the caller happened to see.
type userPreferences struct {
	capability *skillsplugin.Plugin
	path       string

	mu       sync.Mutex
	settings settings.Settings
}

func newUserPreferences(capability *skillsplugin.Plugin, path string, current settings.Settings) *userPreferences {
	return &userPreferences{capability: capability, path: path, settings: current}
}

// SetEnabled records the user's choice about one capability, which is the other
// thing this file carries.
//
// The file is written first and the caller changes the running capability second,
// the same order and the same reason as SetState: a failed write must leave
// nothing changed, so a request that reports failure is one that can be retried
// without wondering what it already did. Whether the capability exists, and
// whether this transition is allowed, is the registry's business — this records
// what the user asked for.
func (c *userPreferences) SetEnabled(id string, enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.settings.WithCapabilityEnabled(id, enabled)
	if err := settings.Save(c.path, next); err != nil {
		return err
	}
	c.settings = next
	return nil
}

// Skills lists every discovered skill with its state, which is what the
// settings page renders.
func (c *userPreferences) Skills() []httpapi.SkillRef {
	statuses := c.capability.Skills()
	refs := make([]httpapi.SkillRef, 0, len(statuses))
	for _, status := range statuses {
		refs = append(refs, skillRef(status))
	}
	return refs
}

// SetState turns one skill off or on, and reports whether a skill by that name
// was discovered at all.
//
// The file is written first and the running selection second. A failed write
// therefore changes nothing, which is the only honest outcome: the other order
// would leave the running state saying "off" while the file still said "on", and
// a restart would quietly turn the skill back on.
//
// A name that was never discovered changes nothing either. The settings file is
// a record of what the user asked for, and asking for something that is not
// there is a mistake worth refusing rather than storing.
func (c *userPreferences) SetState(name string, enabled bool) (httpapi.SkillRef, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	target := strings.TrimSpace(name)
	var status skillsplugin.SkillStatus
	found := false
	for _, candidate := range c.capability.Skills() {
		if candidate.Name == target {
			status, found = candidate, true
			break
		}
	}
	if !found {
		return httpapi.SkillRef{}, false, nil
	}

	next := c.settings.WithSkillDisabled(target, !enabled)
	if err := settings.Save(c.path, next); err != nil {
		return httpapi.SkillRef{}, true, err
	}
	c.settings = next
	// The capability cannot fail here: the name was just found in its own list.
	c.capability.SetDisabled(target, !enabled)

	status.Enabled = enabled
	if enabled {
		status.DisabledReason = ""
	} else {
		status.DisabledReason = skillsplugin.DisabledReasonSetting
	}
	return skillRef(status), true, nil
}

func skillRef(status skillsplugin.SkillStatus) httpapi.SkillRef {
	return httpapi.SkillRef{
		Name:           status.Name,
		Description:    status.Description,
		Scope:          string(status.Scope),
		Enabled:        status.Enabled,
		DisabledReason: status.DisabledReason,
	}
}
