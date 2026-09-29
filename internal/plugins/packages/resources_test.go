package packages

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/skills"
	"github.com/Qaraku/luna-agent/internal/runconfig"
)

func TestPackageSkillsAndPresetsUsePinnedReadOnlyResources(t *testing.T) {
	source := t.TempDir()
	os.MkdirAll(filepath.Join(source, "skills", "workflow"), 0700)
	skillPath := filepath.Join(source, "skills", "workflow", "SKILL.md")
	os.WriteFile(skillPath, []byte("---\nname: workflow\ndescription: Original method\n---\nold body"), 0600)
	manifest := Manifest{Format: 1, ID: "study", Version: "1", Title: "Study", Files: []string{"skills/workflow/SKILL.md"}, Skills: []string{"skills/workflow"}, Presets: []runconfig.Selection{{ID: "research", Title: "Research", Instructions: "Use the installed workflow."}}}
	raw, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(source, ManifestName), raw, 0600)
	registry := packageRegistry()
	manager, _ := OpenManager(t.TempDir(), t.TempDir(), registry)
	library, err := skills.NewManaged(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	library.SetExternalSource(manager)
	registry.Register(library)
	registry.Enable(skills.PluginID)
	first, err := manager.Install(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Activate(context.Background(), Activation{ID: "study", Revision: first.Revision, Expected: first.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	found, err := library.Skills()
	if err != nil || len(found) != 1 || found[0].Managed || found[0].Revision != first.Revision {
		t.Fatalf("catalog=%+v %v", found, err)
	}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Resources: map[string][]string{"skills": {found[0].Name}}, ResourceRevisions: map[string]map[string]string{"skills": {found[0].Name: first.Revision}}})
	os.WriteFile(skillPath, []byte("---\nname: workflow\ndescription: Updated method\n---\nnew body"), 0600)
	second, err := manager.Install(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Activate(context.Background(), Activation{ID: "study", Revision: second.Revision, Expected: first.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	body, err := library.Tools()[0].Invoke(ctx, `{"name":"`+found[0].Name+`"}`)
	if err != nil || body != "old body" {
		t.Fatalf("running skill changed: %q %v", body, err)
	}
	entry, _ := registry.Entry("pkg-study")
	presets, err := entry.Plugin.(plugin.SetupCatalog).Setups()
	if err != nil || len(presets) != 1 || presets[0].Owner != "pkg-study" || presets[0].Revision != second.Revision {
		t.Fatalf("presets=%+v %v", presets, err)
	}
	if err = manager.SetEnabled("study", second.Revision, false); err != nil {
		t.Fatal(err)
	}
	found, err = library.Skills()
	if err != nil || len(found) != 0 {
		t.Fatal("disabled package still exposes new skill selections", err)
	}
	body, err = library.Tools()[0].Invoke(ctx, `{"name":"pkg_study__workflow"}`)
	if err != nil || !strings.Contains(body, "old body") {
		t.Fatal("disable broke a running immutable snapshot", err)
	}
}
