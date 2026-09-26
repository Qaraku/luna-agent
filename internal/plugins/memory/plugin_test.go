package memory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// newPluginForRegistry is the plugin under a fresh temporary state file.
func newPluginForRegistry(t *testing.T) *Plugin {
	t.Helper()
	p, err := New(filepath.Join(t.TempDir(), ".runtime", "memory.jsonl"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// The descriptor is accepted only when it agrees with what the plugin exposes:
// the registry checks both directions, so a passing Register is itself the proof
// that the declaration is honest.
func TestTheDescriptorRegistersWithAGrantedStateWritePermission(t *testing.T) {
	registry := plugin.NewRegistry(plugin.PermissionStateWrite)
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

	// The declared facts budget is the plugin's own 8 KiB line cap plus 1 KiB of
	// headroom for the block title and the Kernel's annotation.
	budget := 0
	for _, c := range descriptor.Contributions {
		if c.Kind == plugin.ContributionContext && c.ID == FactsContextID {
			budget = c.BudgetBytes
		}
	}
	if budget != FactsBudgetBytes || FactsBudgetBytes != MaxInjectBytes+1024 {
		t.Fatalf("facts budget=%d, want %d (%d + 1 KiB)", budget, FactsBudgetBytes, MaxInjectBytes)
	}

	// The two claims are the ones the routes and the data path need.
	wantClaims := []plugin.Claim{
		{Kind: plugin.ClaimRoutePrefix, ID: MemoryRoutePath},
		{Kind: plugin.ClaimStateNamespace, ID: stateNamespace},
	}
	if len(descriptor.Claims) != len(wantClaims) {
		t.Fatalf("claims=%+v", descriptor.Claims)
	}
	for i, claim := range wantClaims {
		if descriptor.Claims[i] != claim {
			t.Fatalf("claims[%d]=%+v, want %+v", i, descriptor.Claims[i], claim)
		}
	}
	if len(descriptor.Permissions) != 1 || descriptor.Permissions[0].Kind != plugin.PermissionStateWrite || descriptor.Permissions[0].Detail != "" {
		t.Fatalf("permissions=%+v", descriptor.Permissions)
	}
}

// Without the grant the same descriptor is refused, and a refused registration
// leaves no residue: no id, no claim.
func TestTheDescriptorIsRejectedWithoutTheStateWriteGrant(t *testing.T) {
	registry := plugin.NewRegistry()
	err := registry.Register(newPluginForRegistry(t))
	if err == nil {
		t.Fatal("Register accepted a state.write permission this registry cannot grant")
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
