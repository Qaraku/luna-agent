package layout

import (
	"path/filepath"
	"strings"
	"testing"
)

// LUNA_HOME is the one-variable answer for development and for anyone who keeps
// a Luna they run as one directory: every root moves under it, so nothing is
// scattered across the home directory and nothing depends on where the process
// was started from.
func TestLunaHomePutsEveryRootUnderOneDirectory(t *testing.T) {
	paths, err := Resolve(envFrom(map[string]string{
		"LUNA_HOME":       "/home/someone/checkout/.runtime",
		"XDG_CONFIG_HOME": "/custom/config",
		"XDG_DATA_HOME":   "/custom/data",
	}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		Config:  "/home/someone/checkout/.runtime",
		Data:    "/home/someone/checkout/.runtime",
		State:   filepath.Join("/home/someone/checkout/.runtime", "state"),
		Cache:   filepath.Join("/home/someone/checkout/.runtime", "cache"),
		Runtime: filepath.Join("/home/someone/checkout/.runtime", "run"),
	}
	if paths != want {
		t.Fatalf("paths = %+v, want %+v", paths, want)
	}
}

// The XDG roots are ignored, not merged: half a configuration from one place and
// half from another is exactly what a single directory is meant to avoid.
func TestLunaHomeIgnoresTheXDGRoots(t *testing.T) {
	paths, err := Resolve(envFrom(map[string]string{
		"LUNA_HOME":      "/dev/luna",
		"XDG_State_Home": "/custom/state",
	}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	if paths.State != "/dev/luna/state" {
		t.Fatalf("State = %q, want it under LUNA_HOME", paths.State)
	}
}

// A relative value here is refused rather than ignored. Ignoring is right for an
// XDG variable that someone else set; this one is a direct request, and a request
// that cannot be honoured silently becomes "Luna put its files somewhere else
// again" — the exact confusion it exists to remove.
func TestLunaHomeMustBeAbsolute(t *testing.T) {
	if _, err := Resolve(envFrom(map[string]string{"LUNA_HOME": "./.runtime"}), "/home/someone"); err == nil {
		t.Fatal("Resolve accepted a relative LUNA_HOME")
	} else if !strings.Contains(err.Error(), "LUNA_HOME") {
		t.Fatalf("error should name the variable, got %v", err)
	}
}

// With LUNA_HOME set there is nothing left for the home directory to decide, so
// an environment that cannot report one must still resolve.
func TestLunaHomeNeedsNoHomeDirectory(t *testing.T) {
	paths, err := Resolve(envFrom(map[string]string{"LUNA_HOME": "/dev/luna"}), "")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != "/dev/luna" {
		t.Fatalf("Config = %q, want /dev/luna", paths.Config)
	}
}
