package plugin_test

import (
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/terminal"
)

// The exec permission is real authority, not a formality: a descriptor that asks for it
// registers only when the registry holds the grant, so allowing command execution is an
// explicit act of the host. This test and the one below are the two halves of that claim
// — with the grant the descriptor is honest and accepted, without it the same descriptor
// is refused.
func TestTheExecDescriptorRegistersWithTheGrant(t *testing.T) {
	registry := plugin.NewRegistry(plugin.PermissionProcessExec)
	if err := registry.Register(terminal.New()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	entry, ok := registry.Entry(terminal.PluginID)
	if !ok {
		t.Fatalf("plugin %q is missing after Register", terminal.PluginID)
	}
	if entry.State != plugin.StateRegistered || entry.Err != nil {
		t.Fatalf("entry = %+v", entry)
	}
	if len(entry.Descriptor.Permissions) != 1 || entry.Descriptor.Permissions[0].Kind != plugin.PermissionProcessExec {
		t.Fatalf("permissions = %+v", entry.Descriptor.Permissions)
	}
	if grants := registry.Grants(); len(grants) != 1 || grants[0] != plugin.PermissionProcessExec {
		t.Fatalf("grants = %v", grants)
	}
}

// Without the grant the same descriptor is refused, and the refusal leaves no residue: no
// id, no exposed tool. A capability cannot widen its own authority by declaring more.
func TestTheExecDescriptorIsRejectedWithoutTheGrant(t *testing.T) {
	registry := plugin.NewRegistry()
	err := registry.Register(terminal.New())
	if err == nil {
		t.Fatal("Register accepted a process.exec permission this registry cannot grant")
	}
	if !strings.Contains(err.Error(), "not authorized to grant") {
		t.Fatalf("err = %q", err)
	}
	if _, ok := registry.Entry(terminal.PluginID); ok {
		t.Fatal("a rejected plugin is present in the registry")
	}
	if entries := registry.Entries(); len(entries) != 0 {
		t.Fatalf("Entries = %d, want 0", len(entries))
	}
}
