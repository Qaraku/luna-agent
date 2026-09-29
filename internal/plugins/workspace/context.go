package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
)

// projectBlockHeader says what the block is. It states the capability's own
// subject and nothing about how the Kernel frames or orders blocks: the
// reference-data disclaimer, the source label and the budget are the Kernel's
// job and are applied to every reference contribution alike.
const projectBlockHeader = "The project this session is working in."

// workspaceBlockHeader is the same statement for a session that is associated
// with a workspace: the block names the workspace rather than the one root the
// file tools happen to be bounded to.
const workspaceBlockHeader = "The workspace this session is working in."

// workspaceBlockBody states what the list of directories is and is not. It says
// they are named by their own directory names because that is what the block
// carries — an absolute host path would pin one machine's layout into every run,
// which is the convention the file tool's description already follows. It does
// not call the list a boundary: a workspace says where work is happening, and
// deciding what may be read stays the file tools' job.
const workspaceBlockBody = "These are the directories this session's work is about. Every one is named by its own directory name; an absolute host path is not part of this statement."

// projectBlockBody states the identity the model needs and where paths come
// from for the fallback, single-root case.
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

// Render is the fallback block: the header, the project line and the body. There
// is no generation, version, process id or credential here, and no field that
// could carry one — none of them belongs in an identity statement.
func (p *Plugin) Render() string {
	var b strings.Builder
	b.WriteString(projectBlockHeader)
	b.WriteString("\nProject: ")
	b.WriteString(p.project)
	b.WriteString("\n")
	b.WriteString(p.projectBlockBody())
	return b.String()
}

// renderWorkspace is the identity block for a session that works in a workspace:
// the header, the workspace's name, and the directory names it is made of. The
// directories' absolute paths are what files are read from, never what the model
// is told; they stay in the Target.
func renderWorkspace(target Target) string {
	names := make([]string, 0, len(target.Dirs))
	for _, dir := range target.Dirs {
		names = append(names, dirName(dir))
	}
	var b strings.Builder
	b.WriteString(workspaceBlockHeader)
	b.WriteString("\nWorkspace: ")
	b.WriteString(target.Name)
	b.WriteString("\nDirectories: ")
	b.WriteString(strings.Join(names, ", "))
	b.WriteString("\n")
	b.WriteString(workspaceBlockBody)
	b.WriteString("\n")
	return b.String()
}

// dirName is a directory's own name, which is all the model is told about it. A
// directory whose last element is not a name — the filesystem root — is rendered
// as itself rather than as "." or "", because "" would say nothing and "." would
// say the wrong thing.
func dirName(dir string) string {
	cleaned := filepath.Clean(dir)
	name := filepath.Base(cleaned)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return cleaned
	}
	return name
}

// rulesSectionLabel introduces one directory's rules. The rules of several
// directories are one instruction block, so each has to say where it came from:
// a rule whose directory is unknown would leave the model unable to tell whose
// rule it is reading. It is a directory name, never a path.
func rulesSectionLabel(dir string) string {
	return "\nRules from \"" + dir + "\":\n"
}

// rulesBlock renders the fallback project-rule contribution, or reports that
// there is none to make. It contributes nothing in three honest cases and never
// a shortened rule set:
//
//   - the project states no rules (the composition root handed over no text);
//   - the rules are blank, which is the same statement made with whitespace;
//   - the rules do not fit the budget this capability declared for them. Cutting
//     them at the budget would put a rule the project never wrote in front of
//     the model, so an over-long set is dropped while the project's own file
//     stays the record of it.
//
// It reads nothing and writes nothing: the text was fixed when the composition
// root built the capability, so a context read cannot fail between two reads of
// the same run.
func (p *Plugin) rulesBlock() (plugin.ContextBlock, bool) {
	text := strings.TrimSpace(p.rules)
	if text == "" {
		return plugin.ContextBlock{}, false
	}
	full := rulesBlockHeader + "\n" + text
	if len(full) > RulesBudgetBytes {
		return plugin.ContextBlock{}, false
	}
	return plugin.ContextBlock{
		ID:   RulesContextID,
		Kind: plugin.ContextInstruction,
		Text: full,
	}, true
}

// workspaceRulesBlock 按工作区顺序纳入完整规则文件；空目录不占预算份额。
// 每个文件的读取仍有上限，累计放不下时只跳过整份文件并报告，绝不截断规则。
func (p *Plugin) workspaceRulesBlock(target Target) (plugin.ContextBlock, bool, error) {
	used := rulesBlockOverhead
	sections := make([]string, 0, len(target.Dirs))
	for _, dir := range target.Dirs {
		text, ok := p.readDirRules(dir, MaxRulesTextBytes)
		if !ok {
			continue
		}
		section := rulesSectionLabel(dirName(dir)) + text
		if len(section) > RulesBudgetBytes-used {
			p.reportProblem(fmt.Sprintf("%s in the workspace directory %q was not contributed: the complete section needs %d bytes, but only %d of the %d-byte rules block remain", RulesFileName, dirName(dir), len(section), RulesBudgetBytes-used, RulesBudgetBytes))
			continue
		}
		sections = append(sections, section)
		used += len(section)
	}
	if len(sections) == 0 {
		return plugin.ContextBlock{}, false, nil
	}
	return plugin.ContextBlock{ID: RulesContextID, Kind: plugin.ContextInstruction, Text: rulesBlockHeader + "\n" + strings.Join(sections, "")}, true, nil
}

// readDirRules reads one directory's rules through internal/fileread, the same
// bounded text read the Skills capability uses for its own root, so a workspace
// directory and a skill directory cannot disagree about what a readable file is.
//
// budget bounds a complete rule file before allocation; a file over it is refused
// rather than read partly, and the refusal is stated to the operator. Nothing
// here reaches the model: the model either reads the directory's rules or is not
// told about them at all.
func (p *Plugin) readDirRules(dir string, budget int) (string, bool) {
	path, err := fileread.Resolve(dir, RulesFileName, budget)
	if err != nil {
		if !errors.Is(err, fileread.ErrNotFound) {
			p.reportProblem(fmt.Sprintf("%s in the workspace directory %q was not contributed: %s", RulesFileName, dirName(dir), err))
		}
		return "", false
	}
	text, err := fileread.Read(path, budget)
	if err != nil {
		p.reportProblem(fmt.Sprintf("%s in the workspace directory %q was not contributed: %s", RulesFileName, dirName(dir), err))
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	return trimmed + "\n", true
}

// sessionWorkspace asks the composition root which workspace this run's session
// works in. The run identity travels in the context, which is how a capability
// learns which session it is serving without being handed the session itself.
//
// A run with no session, a session bound to nothing, and a server with no lookup
// are all the same answer here: there is no workspace, so the fallback identity
// is what this run is told. A session that was bound and whose workspace has
// since been removed also lands here, which is the honest reading — the binding
// names something that is not there any more.
func (p *Plugin) sessionWorkspace(ctx context.Context) (Target, bool) {
	if p.lookup == nil {
		return Target{}, false
	}
	info, ok := plugin.Run(ctx)
	if !ok || info.SessionID == "" {
		return Target{}, false
	}
	target, ok := p.lookup(info.SessionID)
	if !ok || strings.TrimSpace(target.Name) == "" || len(target.Dirs) == 0 {
		return Target{}, false
	}
	return target, true
}

// Contexts renders the blocks this capability contributes.
//
// A session associated with a workspace is told that workspace's name and the
// names of its directories, and reads the rules of each of those directories. A
// session that is not is told what it was told before this capability knew about
// workspaces at all: the fallback project's name and its rules. The reference
// block is always contributed on both paths — a session that cannot say where it
// is working would be indistinguishable from one whose block silently failed.
//
// Unlike the fallback path, the workspace path reads files while rendering, so it
// can fail: a directory whose rules cannot be read does not fail the run (it is
// reported and skipped), but a block that does not fit what the capability
// declared is a failure, because the Kernel would silently cut it. The text
// carries no leading blank line: the Kernel is what decides where a contribution
// lands and what kind of block it is.
func (p *Plugin) Contexts(ctx context.Context) ([]plugin.ContextBlock, error) {
	if target, ok := p.sessionWorkspace(ctx); ok {
		identity := renderWorkspace(target)
		if len(identity) > ProjectBudgetBytes {
			return nil, fmt.Errorf("the workspace identity block is %d bytes, over the %d-byte budget it declares", len(identity), ProjectBudgetBytes)
		}
		blocks := []plugin.ContextBlock{{
			ID:   ProjectContextID,
			Kind: plugin.ContextReference,
			Text: identity,
		}}
		rules, ok, err := p.workspaceRulesBlock(target)
		if err != nil {
			return nil, err
		}
		if ok {
			blocks = append(blocks, rules)
		}
		return blocks, nil
	}
	blocks := []plugin.ContextBlock{{
		ID:   ProjectContextID,
		Kind: plugin.ContextReference,
		Text: p.Render(),
	}}
	if rules, ok := p.rulesBlock(); ok {
		blocks = append(blocks, rules)
	}
	return blocks, nil
}
