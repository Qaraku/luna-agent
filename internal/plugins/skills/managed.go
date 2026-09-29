package skills

import (
	"context"
	"fmt"

	"github.com/Qaraku/luna-agent/internal/plugin"
	catalog "github.com/Qaraku/luna-agent/internal/skills"
)

const ManageToolName = "luna_skill_manage"
const LibraryNamespace = "skill-library"

func NewManaged(stateDir string, found []catalog.Skill, disabled ...string) (*Plugin, error) {
	library, err := OpenLibrary(stateDir)
	if err != nil {
		return nil, err
	}
	p := New(found, disabled...)
	p.library = library
	p.tool.source = p.allSkills
	p.tool.library = library
	if _, err = p.allSkills(context.Background()); err != nil {
		return nil, err
	}
	return p, nil
}
func ManagedDescriptor() plugin.Descriptor {
	d := Descriptor()
	d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionTool, ID: ManageToolName})
	d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimStateNamespace, ID: LibraryNamespace})
	d.Permissions = append(d.Permissions, plugin.Permission{Kind: plugin.PermissionStateWrite})
	d.Claims = append(d.Claims, plugin.Claim{Kind: plugin.ClaimRoutePrefix, ID: LibraryPrefix}, plugin.Claim{Kind: plugin.ClaimPanel, ID: LibraryPanelID})
	d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionPanel, ID: LibraryPanelID})
	for _, path := range libraryPaths() {
		d.Contributions = append(d.Contributions, plugin.Contribution{Kind: plugin.ContributionRoute, ID: path})
	}
	return d
}
func (p *Plugin) allSkills(ctx context.Context) ([]catalog.Skill, error) {
	out := append([]catalog.Skill{}, p.found...)
	if p.external != nil {
		additional, err := p.external.ExternalSkills(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, additional...)
	}
	if p.library == nil {
		return out, nil
	}
	entries, err := p.library.List()
	if err != nil {
		return nil, err
	}
	info, _ := plugin.Run(ctx)
	for _, entry := range entries {
		for _, installed := range out {
			if installed.Name == entry.Name {
				return nil, fmt.Errorf("learned skill %q conflicts with an installed source; rename or remove the external duplicate", entry.Name)
			}
		}
		if revision, ok := info.ResourceRevisions[PluginID][entry.Name]; ok && revision != "" {
			entry, err = p.library.Lookup(entry.Name, revision)
			if err != nil {
				return nil, err
			}
		}
		dir, err := p.library.SafeDirectory(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, catalog.Skill{Name: entry.Name, Description: entry.Description, Dir: dir, Scope: catalog.ScopeUser, Revision: entry.Revision, Owner: PluginID})
	}
	return out, nil
}
func (p *Plugin) RunResourceRevisions(ctx context.Context) (map[string]string, error) {
	found, err := p.allSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, skill := range p.state.on(found) {
		out[skill.Name] = skill.Revision
	}
	return out, nil
}
func (p *Plugin) checkLearnedName(name string) error {
	for _, installed := range p.found {
		if installed.Name == name {
			return fmt.Errorf("installed skill %q is read-only to learning; save your adaptation under a new name", name)
		}
	}
	return nil
}

// ExternalSkillSource 在已有目录中提供只读版本化资源；学习库不覆盖它们。
type ExternalSkillSource interface {
	ExternalSkills(context.Context) ([]catalog.Skill, error)
	VerifySkill(catalog.Skill, string, string) error
}

func (p *Plugin) SetExternalSource(source ExternalSkillSource) {
	p.external = source
	p.tool.source = p.allSkills
	p.tool.external = source
}
