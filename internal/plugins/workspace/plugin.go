// Package workspace 是 Workspace 能力：认领“这次会话在哪个项目里工作”，并陈述该项目
// 的规则。
//
// 完整的 Workspace 含义是 Agent 当前工作的项目／资源边界：项目根、当前项目身份、
// 项目规则、允许的读写范围与项目状态。本切片认领其中两件事——**身份**（一条 reference
// 贡献）与**项目规则**（一条 instruction 贡献）——把它们交给内核装配。文件工具与面板
// 仍属于后续切片，本文档不预告它们的实现。
//
// 边界由内核强制，身份由能力陈述：能力拿到的是内核已经解析好的读根
// （`pluginhost.Options.ReadRoot`），自己不再解析第二套根；模型看到的文本里只有
// 目录名，没有宿主绝对路径。规则文本同样由装配层读好再交给能力：能力自己不开文件，
// 因此不需要内核目前还无法强制的 `filesystem.read` 权限。
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
	// reserve for the project block. The block is a few short lines, so this is
	// headroom rather than a limit: an identity statement that got truncated
	// would tell the model something the capability never said.
	ProjectBudgetBytes = 2 * 1024

	// RulesBudgetBytes is the byte budget for the rules block: the capability's
	// own lead-in line plus the project's rule text. The capability never emits
	// a rules block larger than this. An over-long set of rules is refused, not
	// cut, because a rule that stops mid-sentence is a rule the project never
	// wrote.
	RulesBudgetBytes = 4 * 1024
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
)

// MaxRulesTextBytes is the most rule text the capability accepts. The
// composition root reads a rules file with this ceiling: a file larger than it
// yields no rules rather than a truncated rule set.
const MaxRulesTextBytes = RulesBudgetBytes - rulesBlockOverhead

// Plugin names the project the agent is working in and states that project's
// rules.
//
// It holds two strings and nothing else: identity is derived from the root the
// kernel hands over, and the rules are the text the composition root read and
// passed in. The capability itself opens no file — it has no path to one — so
// it keeps no store, no state directory and no permission. Being built in is a
// deployment choice, not a privilege: this capability declares the same claims
// and asks for the same permissions any other built-in plugin would, which here
// means none of either.
type Plugin struct {
	// project is the project directory's own name, never a path. The block the
	// model reads is built from this field, so a host path cannot reach the
	// model by accident.
	project string
	// rules is the project-rule text, already read from disk by the composition
	// root. Empty means the project states no rules, and then no instruction
	// block is contributed — as opposed to a long set of rules, which is
	// refused rather than silently cut to fit.
	rules string
}

// New binds the capability to the project root it was handed, together with the
// project-rule text the composition root resolved. root is the directory the
// file tool is bounded to — the Kernel resolves it once and both sides use that
// one value — and this package never resolves a root of its own. rules is the
// rule text, or empty when there are none; reading the file that holds it, and
// deciding it is missing, empty or too large, is the composition root's job, so
// this capability never opens a path and needs no filesystem permission.
//
// A root that names no project is a composition error, not a state the
// capability can render honestly, so it fails here rather than contributing a
// blank identity later.
func New(root, rules string) (*Plugin, error) {
	name := projectName(root)
	if name == "" {
		return nil, fmt.Errorf("workspace root %q does not name a project", root)
	}
	return &Plugin{project: name, rules: rules}, nil
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
		// what the Kernel has to enforce for a capability; this slice keeps no
		// state (so no state namespace, and no state.write) and serves no route
		// or panel (so no route-prefix and no panel id). It also does not
		// declare filesystem.read: the composition root reads the rules file and
		// passes the text in, the read boundary belongs to the Kernel, and
		// declaring a permission the Kernel does not enforce would be a claim
		// that means nothing. When a later slice really gives this capability
		// files to own, it declares exactly that then.
	}
}

// Descriptor implements plugin.Plugin.
func (p *Plugin) Descriptor() plugin.Descriptor { return Descriptor() }

// Contexts is the only provider interface this capability implements: one
// reference block naming the project, plus — when the project states rules — one
// instruction block carrying them.
var _ plugin.ContextProvider = (*Plugin)(nil)
