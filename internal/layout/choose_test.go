package layout

import (
	"strings"
	"testing"
)

// ChooseDir is the compatibility rule the composition root uses when a kind of
// data may still live where it used to: the new location wins when it is already
// there, and also when neither location exists (a fresh installation starts in
// the new place), but an installation that still keeps its data in the previous
// location keeps working there. Nothing is moved and nothing is deleted.
func TestChooseDirPrefersTheNewLocationWhenItAlreadyExists(t *testing.T) {
	choice := ChooseDir("sessions", "/new/sessions", "/old/sessions", true, true)
	if choice.Dir != "/new/sessions" {
		t.Fatalf("got %q, want the new location", choice.Dir)
	}
	if !choice.New {
		t.Fatalf("choice.New = false, want true")
	}
	if !strings.Contains(choice.Reason, "/new/sessions") {
		t.Fatalf("reason %q should name the directory it chose", choice.Reason)
	}
}

func TestChooseDirKeepsThePreviousLocationWhileItHoldsData(t *testing.T) {
	choice := ChooseDir("sessions", "/new/sessions", "/old/sessions", false, true)
	if choice.Dir != "/old/sessions" {
		t.Fatalf("got %q, want the previous location", choice.Dir)
	}
	if choice.New {
		t.Fatalf("choice.New = true, want false: the previous location is in use")
	}
	// The reason has to say both where the data is and where it would go: a user
	// reading the log must be able to act on it without guessing.
	if !strings.Contains(choice.Reason, "/old/sessions") || !strings.Contains(choice.Reason, "/new/sessions") {
		t.Fatalf("reason %q should name both locations", choice.Reason)
	}
}

func TestChooseDirCreatesTheNewLocationWhenNeitherExists(t *testing.T) {
	choice := ChooseDir("sessions", "/new/sessions", "/old/sessions", false, false)
	if choice.Dir != "/new/sessions" {
		t.Fatalf("got %q, want the new location", choice.Dir)
	}
	if !choice.New {
		t.Fatalf("choice.New = false, want true")
	}
	if !strings.Contains(choice.Reason, "/new/sessions") {
		t.Fatalf("reason %q should name where it will be created", choice.Reason)
	}
}

// A run can append to a directory that is not there yet, so "exists" means a
// directory that is already usable — not a path that happens to be a file.
func TestChooseDirNeverReportsAnEmptyChoice(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		newExists, oldExists bool
	}{
		{"new", true, false},
		{"old", false, true},
		{"both", true, true},
		{"neither", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choice := ChooseDir("sessions", "/new/sessions", "/old/sessions", tc.newExists, tc.oldExists)
			if choice.Dir == "" || choice.Reason == "" {
				t.Fatalf("choice = %+v, want a directory and a reason", choice)
			}
		})
	}
}
