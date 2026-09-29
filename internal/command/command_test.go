package command

import (
	"strings"
	"testing"
)

func TestLookupResolvesNamesAndAliasesWithoutCase(t *testing.T) {
	table, err := New(Command{
		Name:    "model",
		Summary: "Show or choose the model",
		Usage:   "/model [name]",
		Aliases: []string{"m"},
		Args:    ArgText,
		Busy:    BusyReject,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"model", "MODEL", "  Model ", "m"} {
		cmd, ok := table.Lookup(name)
		if !ok || cmd.Name != "model" {
			t.Fatalf("Lookup(%q) = %+v, %v", name, cmd, ok)
		}
	}
	if _, ok := table.Lookup("missing"); ok {
		t.Fatal("an unknown name resolved")
	}
}

// A name is a promise to the user, so two commands cannot make the same one —
// including through an alias, which would make one of them unreachable.
func TestTwoCommandsCannotPromiseTheSameName(t *testing.T) {
	base := func(name string, aliases ...string) Command {
		return Command{Name: name, Summary: "does something", Aliases: aliases, Args: ArgNone, Busy: BusyAllow}
	}
	cases := []struct {
		name     string
		commands []Command
	}{
		{"same name", []Command{base("model"), base("model")}},
		{"alias shadows a name", []Command{base("model", "m"), base("m")}},
		{"name shadows an alias", []Command{base("m"), base("model", "m")}},
		{"two aliases of the same name", []Command{base("model", "m", "m")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.commands...); err == nil {
				t.Fatal("the collision was accepted")
			}
		})
	}
}

func TestATableRefusesCommandsThatCannotBeShown(t *testing.T) {
	cases := []struct {
		name    string
		command Command
	}{
		{"name with a slash", Command{Name: "/model", Summary: "s", Args: ArgNone, Busy: BusyAllow}},
		{"name with a space", Command{Name: "mo del", Summary: "s", Args: ArgNone, Busy: BusyAllow}},
		{"name with uppercase", Command{Name: "Model", Summary: "s", Args: ArgNone, Busy: BusyAllow}},
		{"no summary", Command{Name: "model", Args: ArgNone, Busy: BusyAllow}},
		{"options without a picker", Command{Name: "model", Summary: "s", Args: ArgText, Options: []Option{{Value: "x"}}, Busy: BusyAllow}},
		{"picker with nothing to choose", Command{Name: "model", Summary: "s", Args: ArgOptions, Busy: BusyAllow}},
		{"option with no value", Command{Name: "model", Summary: "s", Args: ArgOptions, Options: []Option{{Summary: "x"}}, Busy: BusyAllow}},
		{"unknown argument kind", Command{Name: "model", Summary: "s", Args: "whatever", Busy: BusyAllow}},
		{"unknown busy policy", Command{Name: "model", Summary: "s", Args: ArgNone, Busy: "maybe"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.command); err == nil {
				t.Fatalf("%+v was accepted", tc.command)
			}
		})
	}
}

// The interface narrows the list as a user types, and "/" alone lists
// everything: a user who has not typed a command yet is exactly the user who
// needs to see the choices.
func TestCandidateMatchesNamesAndAliasesByPrefix(t *testing.T) {
	cmd := Command{Name: "model", Summary: "s", Aliases: []string{"m"}, Args: ArgNone, Busy: BusyAllow}
	for _, prefix := range []string{"", "m", "mo", "mod", "MODEL"} {
		if !cmd.Candidate(prefix) {
			t.Fatalf("prefix %q did not match", prefix)
		}
	}
	for _, prefix := range []string{"h", "modell", "x"} {
		if cmd.Candidate(prefix) {
			t.Fatalf("prefix %q matched", prefix)
		}
	}
}

// The commands the kernel owns have to satisfy the same rules as any other
// command: they are built through the same constructor, not around it.
func TestTheBuiltinsFormAValidTable(t *testing.T) {
	table, err := New(Builtins()...)
	if err != nil {
		t.Fatalf("the built-in commands do not form a valid table: %v", err)
	}
	help, ok := table.Lookup("help")
	if !ok {
		t.Fatal("help is not in the table")
	}
	if help.Busy != BusyAllow {
		t.Fatalf("help must work while a run is active, got %q", help.Busy)
	}
	if help.Args != ArgNone {
		t.Fatalf("help takes no argument, got %q", help.Args)
	}
}

// SortedNames is what diagnostics and tests use to state what the table
// promises without repeating the list by hand.
func TestSortedNamesIncludesAliasesInOrder(t *testing.T) {
	table, err := New(
		Command{Name: "model", Summary: "s", Aliases: []string{"m"}, Args: ArgNone, Busy: BusyAllow},
		Command{Name: "help", Summary: "s", Args: ArgNone, Busy: BusyAllow},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(table.SortedNames(), ",")
	if got != "help,m,model" {
		t.Fatalf("SortedNames = %q", got)
	}
}

// A table that was never built is still safe to ask: the composition root may
// hand no commands to a server, and that must not panic.
func TestAZeroTableAnswers(t *testing.T) {
	var table *Table
	if _, ok := table.Lookup("help"); ok {
		t.Fatal("a nil table resolved a command")
	}
	if got := table.All(); len(got) != 0 {
		t.Fatalf("All() = %v", got)
	}
	if got := table.SortedNames(); len(got) != 0 {
		t.Fatalf("SortedNames() = %v", got)
	}
}

func TestPresetCommandChangesOnlySessionSetup(t *testing.T) {
	table, err := New(Builtins()...)
	if err != nil {
		t.Fatal(err)
	}
	cmd, ok := table.Lookup("preset")
	if !ok {
		t.Fatal("preset command is missing")
	}
	if cmd.Args != ArgText || cmd.Busy != BusyReject {
		t.Fatalf("command=%+v", cmd)
	}
}
