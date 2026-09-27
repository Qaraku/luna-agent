package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// newPlugin is the capability bound to a project directory named like the
// repository it usually runs in, with no project rules. The directory is never
// created: identity comes from the name the Kernel hands over, the rules come
// from the composition root, and this capability never touches disk.
func newPlugin(t *testing.T, root string) *Plugin {
	t.Helper()
	return newPluginWithRules(t, root, "")
}

// newPluginWithRules is the capability bound to a root and handed rule text, the
// way the composition root builds it after reading the rules file.
func newPluginWithRules(t *testing.T, root, rules string) *Plugin {
	t.Helper()
	p, err := New(Options{Root: root, Rules: rules})
	if err != nil {
		t.Fatalf("New(%q, %q): %v", root, rules, err)
	}
	return p
}

// projectRoot is a root whose last element is the project name.
func projectRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "luna-agent")
}

// The descriptor is accepted by a registry that grants nothing, which is itself
// the proof that this capability needs no permission: a descriptor asking for
// anything ungranted is refused at registration.
func TestTheDescriptorRegistersWithoutAnyGrantedPermission(t *testing.T) {
	registry := plugin.NewRegistry()
	if err := registry.Register(newPlugin(t, projectRoot(t))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	entry, ok := registry.Entry(PluginID)
	if !ok {
		t.Fatalf("capability %q is missing after Register", PluginID)
	}
	if entry.State != plugin.StateRegistered || entry.Err != nil {
		t.Fatalf("state=%q err=%v, want registered with no error", entry.State, entry.Err)
	}

	d := entry.Descriptor
	if d.ID != PluginID || d.Title != PluginTitle || d.Deployment != plugin.DeploymentBuiltin {
		t.Fatalf("descriptor=%+v", d)
	}
	want := []plugin.Contribution{
		{Kind: plugin.ContributionContext, ID: ProjectContextID, BudgetBytes: ProjectBudgetBytes},
		{Kind: plugin.ContributionContext, ID: RulesContextID, BudgetBytes: RulesBudgetBytes},
	}
	if len(d.Contributions) != len(want) {
		t.Fatalf("contributions=%+v, want %+v", d.Contributions, want)
	}
	for i := range want {
		if d.Contributions[i] != want[i] {
			t.Fatalf("contributions=%+v, want %+v", d.Contributions, want)
		}
	}
	// Identity and rules are all this slice contributes: no tool, no route, no
	// panel — and therefore nothing that could carry a serving generation or a
	// plugin process id to the model.
	for _, c := range d.Contributions {
		if c.Kind != plugin.ContributionContext {
			t.Fatalf("capability contributes %s %q, want a context block only", c.Kind, c.ID)
		}
	}
}

// The capability claims no state namespace and asks for no permission. Claiming
// one it has no data for would make the Kernel enforce a boundary for nobody,
// and it must not claim Memory's .runtime either: two capabilities cannot hold
// the same namespace, so a placeholder claim would also be a future collision.
func TestTheCapabilityClaimsNoNamespaceAndAsksForNoPermission(t *testing.T) {
	d := Descriptor()
	if len(d.Claims) != 0 {
		t.Fatalf("claims=%+v, want none", d.Claims)
	}
	if len(d.Permissions) != 0 {
		t.Fatalf("permissions=%+v, want none", d.Permissions)
	}
}

// A root that names nothing is a composition error: there is no honest identity
// to render, so construction fails instead of contributing a blank block.
func TestARootThatNamesNoProjectIsRefused(t *testing.T) {
	for _, root := range []string{"", "/"} {
		if p, err := New(Options{Root: root}); err == nil {
			t.Fatalf("New(%q, \"\") accepted a root with no project name: %+v", root, p)
		}
	}
}

// The project is named by the root's last element, whatever form the root
// arrives in; a relative root must not end up naming the whole path.
func TestTheProjectNameIsTheRootsLastElement(t *testing.T) {
	cases := []struct {
		root string
		want string
	}{
		{"/srv/projects/luna-agent", "luna-agent"},
		{"/srv/projects/luna-agent/", "luna-agent"},
		{"luna-agent", "luna-agent"},
	}
	for _, tc := range cases {
		if got := newPlugin(t, tc.root).project; got != tc.want {
			t.Fatalf("root %q names the project %q, want %q", tc.root, got, tc.want)
		}
	}
}

// Enabling and disabling moves the capability between lifecycle states and
// takes it out of the enabled set the Kernel assembles from; it never touches
// what the capability holds, which for this slice is nothing on disk at all.
func TestDisablingTakesTheCapabilityOutOfServiceAndEnablingBringsItBack(t *testing.T) {
	registry := plugin.NewRegistry()
	p := newPlugin(t, projectRoot(t))
	if err := registry.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := registry.Enable(PluginID); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if enabled := registry.Enabled(); len(enabled) != 1 || enabled[0].Descriptor.ID != PluginID {
		t.Fatalf("enabled=%+v, want this capability alone", enabled)
	}

	if err := registry.Disable(PluginID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if enabled := registry.Enabled(); len(enabled) != 0 {
		t.Fatalf("a disabled capability is still enabled: %+v", enabled)
	}
	entry, ok := registry.Entry(PluginID)
	if !ok || entry.State != plugin.StateDisabled {
		t.Fatalf("entry=%+v ok=%v, want a registered but disabled capability", entry, ok)
	}
	// Disabling is not deletion: the same plugin value still renders its block,
	// and the Kernel is what decides whether that block is used.
	if _, err := p.Contexts(context.Background()); err != nil {
		t.Fatalf("Contexts after disabling: %v", err)
	}

	if err := registry.Enable(PluginID); err != nil {
		t.Fatalf("Enable again: %v", err)
	}
	if enabled := registry.Enabled(); len(enabled) != 1 {
		t.Fatalf("enabled=%+v, want the capability back", enabled)
	}
	if !strings.Contains(p.Render(), "luna-agent") {
		t.Fatalf("the block lost its identity: %q", p.Render())
	}
}

// The capability keeps no state: constructing it and reading its context writes
// nothing anywhere, which is why it can declare no state namespace honestly.
func TestTheCapabilityWritesNothing(t *testing.T) {
	dir := t.TempDir()
	p := newPlugin(t, filepath.Join(dir, "luna-agent"))
	if _, err := p.Contexts(context.Background()); err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the directory the capability was pointed at: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the capability created %d entries in %q", len(entries), dir)
	}
}
