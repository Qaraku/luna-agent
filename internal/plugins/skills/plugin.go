// Package skills 是 Skills 能力：把发现到的 skill 清单交给模型，并让它按需读取其中
// 一个的正文。
//
// 它对应渐进披露的两级：清单（一条 skill 类型的上下文贡献，每个 skill 一行）与正文
// （`luna_skill_view` 工具，读一个 skill 的 SKILL.md 或它目录下的一个文件）。清单进
// 每一次运行，正文只在模型真的要用时被读。
//
// 边界由内核强制：读取复用 `internal/fileread`，根是那个 skill 自己的目录，因此模型
// 给的路径永远走不出一个 skill。能力自己不解析第二套根。它不认领状态命名空间、不
// 声明权限——它只读发现期已经确定的目录，没有任何内核目前无法强制的需求。它也不贡献
// 路由或面板：skill 是用户放在磁盘上的文件，不是这个能力自己的数据。
package skills

import (
	"context"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/skills"
)

const (
	// PluginID is the registry id of the Skills capability.
	PluginID = "skills"
	// PluginTitle is the capability's human-readable title.
	PluginTitle = "Skills"

	// ListContextID is the id of the context contribution that carries the
	// manifest of discovered skills.
	ListContextID = "skills"

	// SkillToolName is the model-visible name of the tool that reads one skill.
	SkillToolName = "luna_skill_view"

	// ListBudgetBytes is the byte budget the capability gives the manifest
	// itself. The manifest drops descriptions before names when it grows past
	// it, and says out loud that it did — see skills.List.
	ListBudgetBytes = skills.DefaultListBudget

	// ListContributionBudgetBytes is what the descriptor asks the Kernel to
	// reserve for the manifest block. It is larger than ListBudgetBytes on
	// purpose: the difference holds the lead-in line and the framing the Kernel
	// puts above every block, so a manifest that fits ListBudgetBytes is never
	// cut a second time by the Kernel.
	ListContributionBudgetBytes = 9 * 1024
)

// listBlockHeader is the capability's own lead-in. What the block *is* — a set of
// procedures — is the assembler's framing, applied to every skill contribution
// alike; this line only says what the lines below are.
const listBlockHeader = "The skills installed here, one line each. A skill is a procedure for a kind of work; when one of them looks relevant to the task in hand, read it with " + SkillToolName + " before starting that kind of work."

// Plugin contributes the manifest and the tool that reads one skill.
//
// It holds the skills discovered at start-up and nothing else: no state
// directory, no route, no panel, and no permission. Being built in is a
// deployment choice, not a privilege: it declares the same contributions and
// asks for the same permissions any other built-in capability would, which here
// means no claims and no permissions at all.
type Plugin struct {
	found []skills.Skill
	tool  *SkillViewTool
}

// New binds the capability to the skills the composition root discovered. A
// discovery with no skills is a normal state, not an error: the capability then
// contributes no context block, and its tool reports that no skill by that name
// is installed.
func New(found []skills.Skill) *Plugin {
	return &Plugin{found: found, tool: NewSkillViewTool(found)}
}

// Descriptor declares exactly what this capability exposes. The registry checks
// both directions, so it has to stay in step with Contexts and Tools: a declared
// contribution that is never exposed is a registration error, not a silent
// no-op.
//
// It is a package-level function as well as a method because the composition
// root needs it before it can build the plugin.
func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionContext, ID: ListContextID, BudgetBytes: ListContributionBudgetBytes},
			{Kind: plugin.ContributionTool, ID: SkillToolName},
		},
		// No claims and no permissions, on purpose. The capability keeps no
		// state (no state namespace, hence no state.write), serves no route and
		// no panel (hence no route-prefix and no panel id), and reads only the
		// directories discovery already resolved: the read boundary belongs to
		// internal/fileread and to the Kernel, not to this capability.
	}
}

// Descriptor implements plugin.Plugin.
func (p *Plugin) Descriptor() plugin.Descriptor { return Descriptor() }

// Tools returns the model-visible surface: exactly luna_skill_view.
func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{p.tool} }

// Contexts renders the manifest of discovered skills, or contributes nothing
// when there are none. It reads nothing at run time: the skills were discovered
// once at start-up, so two reads of the same run cannot disagree.
//
// An empty manifest is no block at all rather than a header with nothing under
// it: telling the model there are skills, and then naming none, would be a
// statement about this installation that is not true.
func (p *Plugin) Contexts(context.Context) ([]plugin.ContextBlock, error) {
	text, _ := skills.List(p.found, ListBudgetBytes)
	if text == "" {
		return nil, nil
	}
	return []plugin.ContextBlock{{
		ID:   ListContextID,
		Kind: plugin.ContextSkill,
		Text: listBlockHeader + "\n" + text,
	}}, nil
}

// The two provider interfaces the descriptor declares.
var (
	_ plugin.ToolProvider    = (*Plugin)(nil)
	_ plugin.ContextProvider = (*Plugin)(nil)
)
