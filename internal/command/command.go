// Package command is the composer's command table: the things a user can type
// with a leading slash and have happen without asking the model.
//
// The table is data rather than a switch statement in the interface. One place
// decides which commands exist and what each one takes, the HTTP contract hands
// that table to the browser, and the browser draws its candidates and lists them
// from it. A command whose shape lived in the browser would have to be described
// twice, and the two descriptions would drift.
//
// Commands are matched by the browser, where the keystrokes are, but the names
// are decided here: a name is a promise to the user, and two commands cannot
// promise the same one.
package command

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// namePattern is what a command name may look like, without its leading slash.
// Lowercase letters, digits and hyphens: a name is typed, so it should not need
// a modifier key, and it doubles as the token a user may write in prose.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ArgKind says what a command takes after its name. It is declared so the host
// can render the right kind of input — a picker for a fixed set, free text for
// an argument — instead of every command bringing its own user interface.
type ArgKind string

const (
	// ArgNone is a command that takes nothing: "/help".
	ArgNone ArgKind = "none"
	// ArgText is a command that takes one free-text argument: "/model X".
	ArgText ArgKind = "text"
	// ArgOptions is a command that takes one of a fixed set, which the host
	// renders as a picker.
	ArgOptions ArgKind = "options"
)

// BusyPolicy says what a command does while a run is already active. Luna runs
// one task at a time, so a command that would change what the next run does must
// either be refused or wait — silently accepting it and applying it somewhere
// else is the one outcome nobody can reason about.
type BusyPolicy string

const (
	// BusyAllow is for commands that do not touch a run: they work while one is
	// in flight.
	BusyAllow BusyPolicy = "allow"
	// BusyReject is for commands that change what a run would do. They are
	// refused while a run is active, with a reason the user can act on.
	BusyReject BusyPolicy = "reject"
)

// Option is one accepted value of an ArgOptions command.
type Option struct {
	Value   string
	Summary string
}

// Command is one entry in the table. Every field here is meant to be shown to a
// user: this is the description the interface renders, not documentation for
// whoever writes the next command.
type Command struct {
	// Name is the command without its leading slash.
	Name string
	// Summary is one line, written so a user can decide whether to run it.
	Summary string
	// Usage shows the argument shape, e.g. "/model <name>".
	Usage string
	// Category groups commands in the list. Empty means ungrouped.
	Category string
	// Aliases are other names that resolve to this command.
	Aliases []string
	// Args is what the command takes after its name.
	Args ArgKind
	// Options are the accepted values when Args is ArgOptions.
	Options []Option
	// Busy is what happens when a run is already active.
	Busy BusyPolicy
}

// Table is the set of commands Luna knows. It is built once, at composition,
// and is read-only afterwards.
type Table struct {
	commands []Command
	byName   map[string]int
}

// New validates the commands and returns the table.
//
// Registration is the only place these mistakes can be caught: two commands
// promising the same name, an alias that shadows another command, a command with
// no summary to show, or an options command with nothing to choose from are all
// things a user would experience as a broken interface rather than as an error.
func New(commands ...Command) (*Table, error) {
	table := &Table{byName: make(map[string]int, len(commands))}
	for _, cmd := range commands {
		if err := validate(cmd); err != nil {
			return nil, err
		}
		// Every name is checked before any is registered, so a command that
		// collides leaves the table exactly as it was. Registering as we go
		// would leave half a command behind on failure.
		names := append([]string{cmd.Name}, cmd.Aliases...)
		seen := make(map[string]bool, len(names))
		for _, name := range names {
			if seen[name] {
				return nil, fmt.Errorf("command %q promises the name %q twice", cmd.Name, name)
			}
			seen[name] = true
			if owner, taken := table.byName[name]; taken {
				return nil, fmt.Errorf("command %q: the name %q is already promised by %q", cmd.Name, name, table.commands[owner].Name)
			}
		}
		for _, name := range names {
			table.byName[name] = len(table.commands)
		}
		table.commands = append(table.commands, cmd)
	}
	return table, nil
}

func validate(cmd Command) error {
	if !namePattern.MatchString(cmd.Name) {
		return fmt.Errorf("command name %q must match %s", cmd.Name, namePattern)
	}
	if strings.TrimSpace(cmd.Summary) == "" {
		return fmt.Errorf("command %q has no summary; a command with nothing to show cannot be chosen", cmd.Name)
	}
	for _, alias := range cmd.Aliases {
		if !namePattern.MatchString(alias) {
			return fmt.Errorf("command %q: alias %q must match %s", cmd.Name, alias, namePattern)
		}
	}
	switch cmd.Args {
	case ArgNone, ArgText:
		if len(cmd.Options) > 0 {
			return fmt.Errorf("command %q takes %s, so it cannot have options", cmd.Name, cmd.Args)
		}
	case ArgOptions:
		if len(cmd.Options) == 0 {
			return fmt.Errorf("command %q takes options but declares none to choose from", cmd.Name)
		}
		for _, option := range cmd.Options {
			if strings.TrimSpace(option.Value) == "" {
				return fmt.Errorf("command %q has an option with no value", cmd.Name)
			}
		}
	default:
		return fmt.Errorf("command %q declares unknown argument kind %q", cmd.Name, cmd.Args)
	}
	switch cmd.Busy {
	case BusyAllow, BusyReject:
	default:
		return fmt.Errorf("command %q declares unknown busy policy %q", cmd.Name, cmd.Busy)
	}
	return nil
}

// All returns the commands in the order they were registered, which is the order
// the interface lists them in.
func (t *Table) All() []Command {
	if t == nil {
		return nil
	}
	return append([]Command{}, t.commands...)
}

// Lookup resolves a name or an alias, case-insensitively, since a user typing
// "/Model" means the same thing as "/model".
func (t *Table) Lookup(name string) (Command, bool) {
	if t == nil {
		return Command{}, false
	}
	i, ok := t.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Command{}, false
	}
	return t.commands[i], true
}

// Candidate reports whether name or one of its aliases starts with prefix, which
// is what the interface uses to narrow the list as a user types. An empty prefix
// matches everything, so "/" alone lists every command.
func (c Command) Candidate(prefix string) bool {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(c.Name, prefix) {
		return true
	}
	for _, alias := range c.Aliases {
		if strings.HasPrefix(alias, prefix) {
			return true
		}
	}
	return false
}

// SortedNames is every name and alias in the table, sorted. It exists for tests
// and diagnostics that need to state what the table promises without repeating
// the list.
func (t *Table) SortedNames() []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t.byName))
	for name := range t.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
