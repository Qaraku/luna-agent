package workspace

import (
	"context"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// projectBlockHeader says what the block is. It states the capability's own
// subject and nothing about how the Kernel frames or orders blocks: the
// reference-data disclaimer, the source label and the budget are the Kernel's
// job and are applied to every reference contribution alike.
const projectBlockHeader = "The project this session is working in."

// projectBlockBody states the identity the model needs and where paths come
// from. It names the project only by its directory name — a host absolute path
// would pin one machine's layout into every run, which is the convention the
// file tool's description already follows.
//
// It says "the read root the file tools are bounded to" because that is the
// same value the Kernel handed this capability: identity and boundary come from
// one resolution, so the sentence cannot drift away from what is enforced.
func (p *Plugin) projectBlockBody() string {
	quoted := "\"" + p.project + "\""
	return "The project root is the directory named " + quoted +
		". It is the read root the file tools are bounded to, so file paths are relative to it;" +
		" an absolute path, or one outside it, is refused.\n"
}

// Render is the whole block as one string: the header, the project line and the
// body. There is no generation, version, process id or credential here, and no
// field that could carry one — none of them belongs in an identity statement.
func (p *Plugin) Render() string {
	var b strings.Builder
	b.WriteString(projectBlockHeader)
	b.WriteString("\nProject: ")
	b.WriteString(p.project)
	b.WriteString("\n")
	b.WriteString(p.projectBlockBody())
	return b.String()
}

// Contexts renders the one reference block this capability contributes.
//
// It reads nothing and writes nothing: identity is fixed when the plugin is
// constructed from the root the Kernel resolved, so a context read cannot fail
// and cannot change between two reads of the same run. The block is always
// contributed — a session that cannot say which project it is in would be
// indistinguishable from one whose block silently failed.
//
// The text carries no leading blank line: the Kernel is what decides where a
// reference contribution lands.
func (p *Plugin) Contexts(context.Context) ([]plugin.ContextBlock, error) {
	return []plugin.ContextBlock{{
		ID:   ProjectContextID,
		Kind: plugin.ContextReference,
		Text: p.Render(),
	}}, nil
}
