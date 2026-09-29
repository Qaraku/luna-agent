// Package workspace 是 Workspace 能力：陈述这次会话在哪些目录里工作，并陈述这些目录
// 各自的规则。
//
// Workspace 的含义是 Agent 当前工作的一组目录（可以是一个，也可以是几个）：项目身份、
// 每个目录的规则、以后文件工具与 terminal 的工作上下文。本切片认领其中两件事——
// **身份**（一条 reference 贡献，写工作区名与各目录的**目录名**）与**规则**（一条
// instruction 贡献，内容取自各目录自己的 `AGENTS.md`）——把它们交给内核装配。
//
// 两条路径，按会话是否关联了工作区分开：
//
//   - 关联了：身份与规则都按那个工作区陈述。会话与工作区的关联由装配层提供的一个查表
//     函数回答（它读会话最后一条 config 记录里的 workspace id），能力自己不开会话文件。
//   - 没有关联：走回退，即今天的行为——装配层解析的单一读根的名字，加上装配层读好的
//     那份规则文本。已有会话因此一字不变。
//
// 边界仍由内核强制，身份仍由能力陈述：回退路径拿到的是内核解析好的读根
// （`pluginhost.Options.ReadRoot`）；工作区路径拿到的是装配层存下的那组目录。两条路
// 都只把**目录名**写进模型可见的文本，宿主绝对路径不进上下文。
//
// 规则文本是能力自己读的，读的是每个目录自己的 `AGENTS.md`，用的还是
// `internal/fileread`——与任何读自己根下文本的地方同一套解析与上限，不另写一套。这
// 不需要新增权限声明：内核目前只能强制 `state.write`，声明一个强制不了的权限正是项目
// 拒绝的形态（见 Descriptor）。
package workspace

import (
	"fmt"
	"path/filepath"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

const (
	// PluginID is the registry id of the Workspace capability.
	PluginID = "workspace"
	// PluginTitle is the capability's human-readable title.
	PluginTitle = "Workspace"

	// ProjectContextID is the id of the context contribution that carries the
	// identity of the project this session is working in.
	ProjectContextID = "project"
	// RulesContextID is the id of the context contribution that carries the
	// project's rules: the statements this project's work must follow. It is an
	// instruction contribution, not reference data.
	RulesContextID = "rules"

	// ProjectBudgetBytes is the byte budget the capability asks the Kernel to
	// reserve for the identity block. The block is a few short lines, so this is
	// headroom rather than a limit: an identity statement that got truncated
	// would tell the model something the capability never said.
	ProjectBudgetBytes = 2 * 1024

	// RulesBudgetBytes is the byte budget for the rules block: the capability's
	// own lead-in line plus the rule text of every directory in the workspace.
	// The capability never emits a rules block larger than this. An over-long set
	// of rules is refused, not cut, because a rule that stops mid-sentence is a
	// rule the project never wrote.
	RulesBudgetBytes = 32 * 1024
)

const (
	// rulesBlockHeader is the capability's own lead-in, above the project's rule
	// text. What the block *is* — a project rule to follow — is the assembler's
	// framing and is applied to every instruction contribution alike.
	rulesBlockHeader = "The rules this project's work follows."

	// rulesBlockOverhead is the most the capability adds around the project's
	// own words: the lead-in line and the newline that ends it. It turns the
	// block budget into a text ceiling the composition root can read a rules
	// file with, so a file this capability would refuse is never loaded whole.
	rulesBlockOverhead = len(rulesBlockHeader) + 1

	// RulesFileName is the file a directory states its own rules in. The
	// capability looks for exactly this name in each directory of the workspace,
	// the same way main.go looks for it at the fallback root.
	RulesFileName = "AGENTS.md"
)

// MaxRulesTextBytes is the most rule text the capability accepts — the whole
// text of one directory's rules, or of the fallback root's. The composition root
// reads the fallback rules file with this ceiling: a file larger than it yields
// no rules rather than a truncated rule set. 多目录按完整文件的实际占用共享规则块，
// 没有规则的目录不预留份额。
const MaxRulesTextBytes = RulesBudgetBytes - rulesBlockOverhead

// Target is a workspace as this capability needs it: the name to show and the
// directories to look for rules in. It is not the stored workspace and the
// capability keeps no store: it is what the composition root's lookup answered
// for one session.
type Target struct {
	// Name is the label the user gave the workspace. It is shown to the model as
	// the identity of where the session works.
	Name string
	// Dirs are absolute paths. They never reach the model — only their own base
	// names do — so this field exists for reading files, not for rendering.
	Dirs []string
}

// Lookup answers which workspace a session works in, or reports that it works in
// none. The composition root supplies it: the association lives in the session's
// own config record, which the capability has no way to read and no business
// reading.
type Lookup func(sessionID string) (Target, bool)

// Reporter receives a problem statement when rules cannot be contributed. The
// composition root supplies it (the operator's log); a capability with no
// reporter drops the statement. It is for the operator, never for the model:
// what the model sees is either the rules or nothing, because a half-explained
// rule set is worse than none.
type Reporter func(problem string)

// Options is what the capability is constructed from: the fallback identity, and
// how to find a session's workspace.
type Options struct {
	// Root is the directory the file tools are bounded to. It names the
	// fallback project and is never used when the session has a workspace.
	Root string
	// Rules is the fallback project-rule text, already read by the composition
	// root. Empty means the project states no rules.
	Rules string
	// Lookup answers which workspace a session works in. A nil Lookup means no
	// session has one, which is the state before any workspace is defined.
	Lookup Lookup
	// Report receives rules problems for the operator. Nil drops them.
	Report Reporter
}

// Plugin names where the agent is working and states the rules of that place.
//
// It holds the fallback identity, the fallback rule text, and the two functions
// the composition root handed it; it keeps no state of its own, so it declares
// no state namespace and asks for no permission. Being built in is a deployment
// choice, not a privilege: this capability declares the same claims and asks for
// the same permissions any other built-in plugin would, which here means none of
// either.
type Plugin struct {
	// project is the fallback project directory's own name, never a path. The
	// block the model reads is built from this field, so a host path cannot
	// reach the model by accident.
	project string
	// rules is the fallback project-rule text, already read from disk by the
	// composition root. Empty means the project states no rules, and then no
	// instruction block is contributed — as opposed to a long set of rules,
	// which is refused rather than silently cut to fit.
	rules  string
	lookup Lookup
	report Reporter
}

// New binds the capability to the fallback root and rule text the composition
// root resolved, plus how to find a session's workspace.
//
// opts.Root is the directory the file tool is bounded to — the Kernel resolves it
// once and both sides use that one value — and this package never resolves a root
// of its own. opts.Rules is the fallback rule text, or empty when there are none;
// reading that file, and deciding it is missing, empty or too large, is the
// composition root's job.
//
// A root that names no project is a composition error, not a state the capability
// can render honestly, so it fails here rather than contributing a blank identity
// later. The fallback identity is required even when every session has a
// workspace: it is what an unassociated session is told, and inventing one at run
// time would be inventing an identity.
func New(opts Options) (*Plugin, error) {
	name := projectName(opts.Root)
	if name == "" {
		return nil, fmt.Errorf("workspace root %q does not name a project", opts.Root)
	}
	return &Plugin{project: name, rules: opts.Rules, lookup: opts.Lookup, report: opts.Report}, nil
}

// projectName derives the project's name from a root. Only the last element is
// used: the model-visible block must not carry an absolute host path, the same
// convention the file tool's description follows. Turning the root absolute is
// not a second root resolution — which directory to use was decided by the
// caller — it only makes the last element meaningful for roots like "." or "".
func projectName(root string) string {
	if root == "" {
		return ""
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	name := filepath.Base(filepath.Clean(absolute))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return ""
	}
	return name
}

// reportProblem hands a problem statement to the composition root, if it gave
// one. It is the operator-facing half of "refused, not truncated": the model sees
// no rules at all, and the person running Luna is told why.
func (p *Plugin) reportProblem(problem string) {
	if p.report != nil {
		p.report(problem)
	}
}

// Descriptor declares exactly what this capability exposes. The registry checks
// both directions, so it has to stay in step with Contexts: a declared context
// id that Contexts never uses is a registration error, not a silent no-op.
//
// It is a package-level function as well as a method because the composition
// root needs it before it can build the plugin.
func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionContext, ID: ProjectContextID, BudgetBytes: ProjectBudgetBytes},
			{Kind: plugin.ContributionContext, ID: RulesContextID, BudgetBytes: RulesBudgetBytes},
		},
		// No claims and no permissions, on purpose. Claims and permissions are
		// what the Kernel has to enforce for a capability; this capability keeps
		// no state (so no state namespace, and no state.write) and serves no
		// route or panel (so no route-prefix and no panel id). It does read
		// AGENTS.md files now, but through internal/fileread, which is the same
		// bounded text read the Skills capability uses for its SKILL.md files —
		// so it still does not declare filesystem.read: the Kernel only enforces
		// the permissions it can, and declaring a permission that nothing checks
		// would be a claim that means nothing.
	}
}

// Descriptor implements plugin.Plugin.
func (p *Plugin) Descriptor() plugin.Descriptor { return Descriptor() }

// Contexts is the only provider interface this capability implements: one
// reference block naming where the session works, plus — when rules were found —
// one instruction block carrying them.
var _ plugin.ContextProvider = (*Plugin)(nil)
