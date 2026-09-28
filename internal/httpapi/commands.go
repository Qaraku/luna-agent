package httpapi

import (
	"net/http"

	"github.com/Qaraku/luna-agent/internal/command"
)

// WithCommands supplies the command table the composer works from: the browser
// draws its candidates and its help list from what this server reports, so the
// table has one owner instead of one per interface.
func WithCommands(table *command.Table) Option {
	return func(s *Server) { s.commands = table }
}

// commandView is one command as the browser sees it. It carries only what an
// interface renders, in the shape that interface needs, rather than the Go type:
// the table is the kernel's business, and the wire format is a contract with the
// browser that should be able to change without touching both.
type commandView struct {
	Name     string       `json:"name"`
	Summary  string       `json:"summary"`
	Usage    string       `json:"usage"`
	Category string       `json:"category,omitempty"`
	Aliases  []string     `json:"aliases,omitempty"`
	Args     string       `json:"args"`
	Options  []optionView `json:"options,omitempty"`
	Busy     string       `json:"busy"`
}

type optionView struct {
	Value   string `json:"value"`
	Summary string `json:"summary,omitempty"`
}

type commandsResponse struct {
	Commands []commandView `json:"commands"`
}

func (s *Server) sendCommands(w http.ResponseWriter) {
	send(w, 200, commandsResponse{Commands: s.commandViews()})
}

// commandViews renders the table the composer works from.
//
// /model is derived here rather than handed in with the table, because which
// models exist is read from the provider file and that file can change while this
// process runs: a table built once at startup would offer the models that existed
// then. A table that already declares /model keeps it — the kernel's own list is
// never second-guessed — which also keeps a test's fixture table intact.
func (s *Server) commandViews() []commandView {
	commands := s.commands.All()
	if names := s.liveModelNames(); len(names) > 0 && !declaresModel(commands) {
		commands = append(commands, command.ModelCommand(names))
	}
	return viewsFor(commands)
}

// liveModelNames are the models /model may switch to right now. An empty list
// means the command does not exist at all: a command that offers no choice is not
// a command.
func (s *Server) liveModelNames() []string {
	refs, _, err := s.modelsNow()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(refs))
	for _, model := range refs {
		names = append(names, model.Name)
	}
	return names
}

// declaresModel reports whether the table already carries /model.
func declaresModel(commands []command.Command) bool {
	for _, cmd := range commands {
		if cmd.Name == "model" {
			return true
		}
	}
	return false
}

// viewsFor renders commands as the browser sees them. An empty list is an empty
// list rather than a missing field, so a browser never has to tell "no commands"
// apart from "nothing was sent".
func viewsFor(commands []command.Command) []commandView {
	views := make([]commandView, 0, len(commands))
	for _, cmd := range commands {
		view := commandView{
			Name:     cmd.Name,
			Summary:  cmd.Summary,
			Usage:    cmd.Usage,
			Category: cmd.Category,
			Aliases:  cmd.Aliases,
			Args:     string(cmd.Args),
			Busy:     string(cmd.Busy),
		}
		for _, option := range cmd.Options {
			view.Options = append(view.Options, optionView{Value: option.Value, Summary: option.Summary})
		}
		views = append(views, view)
	}
	return views
}
