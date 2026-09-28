package main

import (
	"path/filepath"
	"testing"

	"github.com/Qaraku/luna-agent/internal/layout"
)

// LUNA_HOME says "everything, here". The previous location must not get a vote:
// a checkout that has ever run has `.runtime/sessions` in it, so a compatibility
// rule applied on top of an explicit request would make the one variable that
// promises one directory unusable exactly where someone reaches for it.
func TestPinnedLocationsIgnoreTheCheckout(t *testing.T) {
	home := "/home/someone/checkout/.runtime"
	sessions, state := pinnedLocations(layout.Paths{Data: home, State: filepath.Join(home, "state")})
	if want := filepath.Join(home, "sessions"); sessions != want {
		t.Fatalf("sessions = %q, want %q", sessions, want)
	}
	if want := filepath.Join(home, "state"); state != want {
		t.Fatalf("state root = %q, want %q", state, want)
	}
}

// The two locations are named by the same variable, so neither can be answered
// by looking at the other one.
func TestPinnedLocationsStayUnderTheNamedDirectory(t *testing.T) {
	home := "/dev/luna"
	sessions, state := pinnedLocations(layout.Paths{Data: home, State: filepath.Join(home, "state")})
	if !filepath.IsAbs(sessions) || !filepath.IsAbs(state) {
		t.Fatalf("both must be absolute: %q, %q", sessions, state)
	}
	if rel, err := filepath.Rel(home, sessions); err != nil || rel == ".." {
		t.Fatalf("sessions %q is not under %q", sessions, home)
	}
	if rel, err := filepath.Rel(home, state); err != nil || rel == ".." {
		t.Fatalf("state %q is not under %q", state, home)
	}
}
