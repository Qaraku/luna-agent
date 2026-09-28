package filewrite

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/settings"
)

// newPluginForRegistry is the plugin under a settings file that need not exist:
// nothing at registration time reads it.
func newPluginForRegistry(t *testing.T) *Plugin {
	t.Helper()
	return New(filepath.Join(t.TempDir(), settings.FileName))
}

// The descriptor is accepted only when it agrees with what the plugin exposes,
// and only when the registry is authorized to grant what it asks for. The
// registry checks both directions, so a passing Register is itself the proof
// that the declaration is honest.
func TestTheDescriptorRegistersWithAGrantedFilesystemWritePermission(t *testing.T) {
	registry := plugin.NewRegistry(plugin.PermissionFilesystemWrite)
	if err := registry.Register(newPluginForRegistry(t)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	entry, ok := registry.Entry(PluginID)
	if !ok {
		t.Fatalf("plugin %q is missing after Register", PluginID)
	}
	if entry.State != plugin.StateRegistered {
		t.Fatalf("state=%q, want %q", entry.State, plugin.StateRegistered)
	}
	if entry.Err != nil {
		t.Fatalf("Err=%v, want nil", entry.Err)
	}
	descriptor := entry.Descriptor
	if descriptor.Title != PluginTitle || descriptor.Deployment != plugin.DeploymentBuiltin {
		t.Fatalf("descriptor=%+v", descriptor)
	}

	// The permission kind is a public name: the setting a user edits and the
	// grant the composition root passes are the same string.
	if plugin.PermissionFilesystemWrite != "fs.write" {
		t.Fatalf("the permission kind is %q, want %q", plugin.PermissionFilesystemWrite, "fs.write")
	}
	if len(descriptor.Permissions) != 1 {
		t.Fatalf("permissions=%+v, want exactly one", descriptor.Permissions)
	}
	if got := descriptor.Permissions[0]; got.Kind != plugin.PermissionFilesystemWrite || got.Detail != "" {
		t.Fatalf("permission=%+v, want {Kind: %q, Detail: \"\"}", got, plugin.PermissionFilesystemWrite)
	}

	// The capability keeps no state and serves no route or panel, so it declares
	// no claims: an unjustified claim is one the Kernel would have to enforce
	// something about for no reason.
	if len(descriptor.Claims) != 0 {
		t.Fatalf("claims=%+v, want none", descriptor.Claims)
	}
	if len(descriptor.Contributions) != 1 || descriptor.Contributions[0].Kind != plugin.ContributionTool || descriptor.Contributions[0].ID != WriteToolName {
		t.Fatalf("contributions=%+v", descriptor.Contributions)
	}
}

// Without the grant the same descriptor is refused, and a refused registration
// leaves no residue: no id, no claim, nothing enabled.
func TestTheDescriptorIsRejectedWithoutTheFilesystemWriteGrant(t *testing.T) {
	registry := plugin.NewRegistry()
	err := registry.Register(newPluginForRegistry(t))
	if err == nil {
		t.Fatal("Register accepted an fs.write permission this registry cannot grant")
	}
	if !strings.Contains(err.Error(), "not authorized to grant") {
		t.Fatalf("err=%q", err)
	}
	if _, ok := registry.Entry(PluginID); ok {
		t.Fatal("a rejected plugin is present in the registry")
	}
	if entries := registry.Entries(); len(entries) != 0 {
		t.Fatalf("Entries=%d, want 0", len(entries))
	}
}

// A grant of some other kind is not this grant: the registry has to be
// authorized for fs.write in particular.
func TestAnotherGrantDoesNotAdmitTheFilesystemWritePermission(t *testing.T) {
	registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionProcessExec)
	if err := registry.Register(newPluginForRegistry(t)); err == nil {
		t.Fatal("Register accepted fs.write under grants that do not include it")
	}
}

// The tools are exactly the tools the descriptor declares: the pairing is
// derived from the declaration, so adding or removing a tool cannot leave the
// declaration behind.
func TestTheToolsAreBoundToThePluginDescriptor(t *testing.T) {
	p := newPluginForRegistry(t)
	declared := map[string]bool{}
	for _, c := range p.Descriptor().Contributions {
		if c.Kind == plugin.ContributionTool {
			declared[c.ID] = true
		}
	}
	tools := p.Tools()
	if len(tools) != len(declared) || len(tools) != 1 {
		t.Fatalf("the plugin exposes %d tools, the descriptor declares %d", len(tools), len(declared))
	}
	if name := tools[0].Name(); !declared[name] || name != WriteToolName {
		t.Fatalf("the plugin exposes %q, want %q", name, WriteToolName)
	}
	// The descriptor function and the method are the same declaration: the
	// composition root reads one and the registry the other.
	if got, want := p.Descriptor(), Descriptor(); got.ID != want.ID || len(got.Contributions) != len(want.Contributions) || len(got.Permissions) != len(want.Permissions) {
		t.Fatalf("Descriptor()=%+v, method=%+v", want, got)
	}
}
