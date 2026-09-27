package layout

import (
	"path/filepath"
	"strings"
	"testing"
)

// envFrom turns a map into the lookup function Resolve takes, so these tests
// describe an environment without touching the real one.
func envFrom(values map[string]string) Env {
	return func(name string) string { return values[name] }
}

func TestEveryRootHonoursItsVariable(t *testing.T) {
	paths, err := Resolve(envFrom(map[string]string{
		"XDG_CONFIG_HOME": "/custom/config",
		"XDG_DATA_HOME":   "/custom/data",
		"XDG_STATE_HOME":  "/custom/state",
		"XDG_CACHE_HOME":  "/custom/cache",
		"XDG_RUNTIME_DIR": "/custom/run",
	}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		Config:  "/custom/config/luna",
		Data:    "/custom/data/luna",
		State:   "/custom/state/luna",
		Cache:   "/custom/cache/luna",
		Runtime: "/custom/run/luna",
	}
	if paths != want {
		t.Fatalf("paths = %+v, want %+v", paths, want)
	}
}

// The specification calls a relative value invalid, so it must not be used: a
// relative XDG_CONFIG_HOME would put the user's configuration somewhere that
// depends on the directory the process happened to start in.
func TestUnsetAndRelativeValuesFallBackToTheDefaults(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"unset", map[string]string{}},
		{"empty", map[string]string{"XDG_CONFIG_HOME": "", "XDG_DATA_HOME": "", "XDG_STATE_HOME": "", "XDG_CACHE_HOME": ""}},
		{"relative", map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "./state", "XDG_CACHE_HOME": "../cache"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := Resolve(envFrom(tc.env), "/home/someone")
			if err != nil {
				t.Fatal(err)
			}
			want := Paths{
				Config: "/home/someone/.config/luna",
				Data:   "/home/someone/.local/share/luna",
				State:  "/home/someone/.local/state/luna",
				Cache:  "/home/someone/.cache/luna",
			}
			if paths != want {
				t.Fatalf("paths = %+v, want %+v", paths, want)
			}
		})
	}
}

// Living with the defaults must not depend on the environment being clean: an
// unset variable and an explicit default are the same location.
func TestAnExplicitDefaultIsTheSamePathAsAnUnsetVariable(t *testing.T) {
	explicit, err := Resolve(envFrom(map[string]string{
		"XDG_CONFIG_HOME": "/home/someone/.config",
		"XDG_DATA_HOME":   "/home/someone/.local/share",
	}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	unset, err := Resolve(envFrom(map[string]string{}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	if explicit != unset {
		t.Fatalf("explicit defaults %+v differ from unset %+v", explicit, unset)
	}
}

// XDG_RUNTIME_DIR is the one variable with no default: its absence means this
// session has no place for process-scoped files. Falling back to an arbitrary
// location would leave sockets behind after a logout.
func TestRuntimeIsEmptyWithoutItsVariableAndFallsBackToState(t *testing.T) {
	paths, err := Resolve(envFrom(map[string]string{}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Runtime != "" {
		t.Fatalf("runtime = %q, want empty", paths.Runtime)
	}
	dir, fellBack := paths.RuntimeOrState()
	if !fellBack || dir != paths.State {
		t.Fatalf("RuntimeOrState() = %q, %v; want the state directory and a reported fallback", dir, fellBack)
	}

	withRuntime, err := Resolve(envFrom(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	dir, fellBack = withRuntime.RuntimeOrState()
	if fellBack || dir != "/run/user/1000/luna" {
		t.Fatalf("RuntimeOrState() = %q, %v; want the runtime directory and no fallback", dir, fellBack)
	}
}

func TestAnUnknownHomeIsAnErrorRatherThanAGuess(t *testing.T) {
	for _, home := range []string{"", "   ", "relative/home"} {
		if _, err := Resolve(envFrom(map[string]string{}), home); err == nil {
			t.Fatalf("home %q was accepted; a home directory Luna cannot place is not a state it can render honestly", home)
		}
	}
}

// Every path this package returns is absolute, so a later join cannot end up
// somewhere that depends on the working directory.
func TestEveryResolvedPathIsAbsolute(t *testing.T) {
	paths, err := Resolve(envFrom(map[string]string{"XDG_CONFIG_HOME": "/custom/config"}), "/home/someone")
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"Config": paths.Config, "Data": paths.Data, "State": paths.State, "Cache": paths.Cache,
	} {
		if !filepath.IsAbs(path) || strings.HasSuffix(path, "/") {
			t.Fatalf("%s = %q, want an absolute path without a trailing separator", name, path)
		}
	}
}
