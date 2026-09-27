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
	send(w, 200, commandsResponse{Commands: commandViews(s.commands)})
}

// commandViews renders the table. An empty table is an empty list rather than a
// missing field, so a browser never has to tell "no commands" apart from
// "nothing was sent".
func commandViews(table *command.Table) []commandView {
	views := make([]commandView, 0, len(table.All()))
	for _, cmd := range table.All() {
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
