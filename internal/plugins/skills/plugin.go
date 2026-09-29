// Package skills 提供按需读取的程序性知识。正式应用同时启用个人学习库；
// 只读构造保留给不需要学习与持久化的嵌入方。
package skills

import (
	"context"
	"strings"

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
// 只读构造保留外部发现结果；管理型构造另持有自己的修订库并声明状态写入、
// 路由、面板与管理工具。两种部署都遵守同一套能力声明与运行授权。
//
// Turning a skill off is not a lifecycle state change of this capability: the
// capability stays in service, its manifest and its tool stay declared, and one
// name leaves the set of skills they read. That is why it is a setting the user
// owns rather than a state the registry keeps.
type Plugin struct {
	found   []skills.Skill
	state   *selection
	tool    *SkillViewTool
	library *Library
}

// SkillStatus is one discovered skill and whether it is in service, as anything
// outside the capability sees it. It carries no directory: where a skill lives
// on this machine is not something an interface needs to show.
type SkillStatus struct {
	Name        string
	Description string
	Scope       skills.Scope
	Enabled     bool
	Revision    string
	Managed     bool
	// DisabledReason says why the skill is not in service, written for the
	// person who turned it off. It is empty for a skill that is on.
	DisabledReason string
}

// DisabledReasonSetting is what a disabled skill's status says: the user turned
// it off in their own settings file. A skill can be out of service for exactly
// one reason today, so the reason is a constant rather than a computed
// sentence.
const DisabledReasonSetting = "已在设置里停用"

// New binds the capability to the skills the composition root discovered and to
// the names the user has turned off in settings. A discovery with no skills is a
// normal state, not an error: the capability then contributes no context block,
// and its tool reports that no skill by that name is installed. A disabled name
// that was never discovered is ignored — the settings file may hold names whose
// directory was removed.
func New(found []skills.Skill, disabled ...string) *Plugin {
	state := newSelection(disabled...)
	return &Plugin{found: found, state: state, tool: NewSkillViewTool(found, state)}
}

// Skills returns every discovered skill and whether it is in service, in
// discovery order. It reads the current selection and changes nothing: the
// interface uses it to show the list and to answer whether a name exists.
func (p *Plugin) Skills() ([]SkillStatus, error) {
	found, err := p.allSkills(context.Background())
	if err != nil {
		return nil, err
	}
	statuses := make([]SkillStatus, 0, len(found))
	for _, skill := range found {
		status := SkillStatus{Name: skill.Name, Description: skill.Description, Scope: skill.Scope, Enabled: !p.state.off(skill.Name), Revision: skill.Revision, Managed: skill.Revision != ""}
		if !status.Enabled {
			status.DisabledReason = DisabledReasonSetting
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// SetDisabled turns one discovered skill off or on and reports whether a skill
// by that name exists here. A name that was never discovered is not stored: the
// interface cannot turn off something the capability cannot see, so the answer
// is the same one the tool would give the model.
//
// This is the only way the set changes, and both the manifest and the tool read
// it when they are next asked — the manifest once per run, the tool once per
// call. A toggle is therefore in effect for the next run without rebuilding the
// agent or restarting anything.
func (p *Plugin) SetDisabled(name string, disabled bool) bool {
	target := strings.TrimSpace(name)
	found := false
	available, err := p.allSkills(context.Background())
	if err != nil {
		return false
	}
	for _, skill := range available {
		if skill.Name == target {
			found = true
			break
		}
	}
	if target == "" || !found {
		return false
	}
	p.state.set(target, disabled)
	return true
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
func (p *Plugin) Descriptor() plugin.Descriptor {
	if p.library != nil {
		return ManagedDescriptor()
	}
	return Descriptor()
}

// Tools 返回声明过的工具；管理型实例额外提供受审批约束的学习工具。
func (p *Plugin) Tools() []plugin.Tool {
	tools := []plugin.Tool{p.tool}
	if p.library != nil {
		tools = append(tools, manageTool{p})
	}
	return tools
}

// Contexts 只提供名字和描述。受管理技能的描述与路径遵守本轮冻结修订，
// 正文仍按需读取；目录损坏要报告，不能假装没有技能。
func (p *Plugin) Contexts(ctx context.Context) ([]plugin.ContextBlock, error) {
	selected := make([]skills.Skill, 0)
	found, err := p.allSkills(ctx)
	if err != nil {
		return nil, err
	}
	for _, skill := range p.state.on(found) {
		if plugin.ResourceSelected(ctx, PluginID, skill.Name) {
			selected = append(selected, skill)
		}
	}
	text, _ := skills.List(selected, ListBudgetBytes)
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

// RunResources 只提供名字；宿主负责冻结本轮选择，正文仍按需读取。
func (p *Plugin) RunResources(ctx context.Context) ([]string, error) {
	names := make([]string, 0)
	found, err := p.allSkills(ctx)
	if err != nil {
		return nil, err
	}
	for _, skill := range p.state.on(found) {
		names = append(names, skill.Name)
	}
	return names, nil
}

// ApplyDisabled 供已验证名称的可信偏好事务使用；写盘后只更新内存，不进行第二次可能失败的目录读取。
func (p *Plugin) ApplyDisabled(name string, disabled bool) { p.state.set(name, disabled) }
