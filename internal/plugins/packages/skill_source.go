package packages

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	catalog "github.com/Qaraku/luna-agent/internal/skills"
)

func splitPackageSkill(name string) (string, string, bool) {
	rest, ok := strings.CutPrefix(name, "pkg_")
	if !ok {
		return "", "", false
	}
	id, local, ok := strings.Cut(rest, "__")
	return id, local, ok && packageName.MatchString(id) && localName.MatchString(local)
}
func (m *Manager) ExternalSkills(ctx context.Context) ([]catalog.Skill, error) {
	run, _ := plugin.Run(ctx)
	versions := map[string]Version{}
	for _, entry := range m.registry.Enabled() {
		cap, ok := entry.Plugin.(*Capability)
		if !ok || cap.store.dir != m.store.dir || !run.Selection.AllowsCapability(entry.Descriptor.ID) {
			continue
		}
		versions[cap.version.Manifest.ID+":"+cap.version.Revision] = cap.version
	}
	for name, revision := range run.ResourceRevisions["skills"] {
		id, _, ok := splitPackageSkill(name)
		if !ok || !run.Selection.AllowsCapability("pkg-"+id) {
			continue
		}
		v, err := m.store.Version(id, revision)
		if err != nil {
			return nil, err
		}
		versions[id+":"+revision] = v
	}
	keys := make([]string, 0, len(versions))
	for key := range versions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []catalog.Skill{}
	seen := map[string]bool{}
	for _, key := range keys {
		v := versions[key]
		for _, dir := range v.Manifest.Skills {
			name := v.Manifest.SkillName(filepath.Base(dir))
			if selected, ok := run.ResourceRevisions["skills"][name]; ok && selected != v.Revision {
				continue
			}
			if seen[name] {
				return nil, fmt.Errorf("duplicate package skill alias")
			}
			seen[name] = true
			skill, err := m.packageSkill(v, name, dir)
			if err != nil {
				return nil, err
			}
			out = append(out, skill)
		}
	}
	return out, nil
}
func (m *Manager) packageSkill(v Version, name, dir string) (catalog.Skill, error) {
	verified, err := m.store.Version(v.Manifest.ID, v.Revision)
	if err != nil {
		return catalog.Skill{}, err
	}
	root, err := fileread.ResolveDir(verified.Root, dir)
	if err != nil {
		return catalog.Skill{}, err
	}
	path, err := fileread.Resolve(root, catalog.FileName, catalog.MaxSkillFileBytes)
	if err != nil {
		return catalog.Skill{}, err
	}
	text, err := fileread.Read(path, catalog.MaxSkillFileBytes)
	if err != nil {
		return catalog.Skill{}, err
	}
	skill, err := catalog.ParseDefinition(filepath.Base(dir), root, []byte(text))
	if err != nil {
		return catalog.Skill{}, err
	}
	skill.Name = name
	skill.Scope = catalog.Scope("package")
	skill.Revision = v.Revision
	skill.Owner = v.Manifest.CapabilityID()
	return skill, nil
}
func (m *Manager) VerifySkill(skill catalog.Skill, relative, body string) error {
	id, local, ok := splitPackageSkill(skill.Name)
	if !ok || skill.Owner != "pkg-"+id {
		return fmt.Errorf("invalid external skill identity")
	}
	v, err := m.store.Version(id, skill.Revision)
	if err != nil {
		return err
	}
	path := filepath.ToSlash(filepath.Join("skills", local, relative))
	for _, file := range v.Files {
		if file.Path == path && file.Hash == digest([]byte(body)) {
			return nil
		}
	}
	return fmt.Errorf("package skill file does not match its immutable revision")
}
