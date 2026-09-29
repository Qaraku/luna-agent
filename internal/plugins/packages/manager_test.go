package packages

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

func packageRegistry() *plugin.Registry {
	return plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionProcessExec, plugin.PermissionFilesystemWrite, plugin.PermissionNetworkFetch)
}
func TestManagerInstallDoesNotEnableAndFailedUpgradeKeepsOld(t *testing.T) {
	requirePackageSandbox(t)
	_, fixture := protocolPackage(t)
	registry := packageRegistry()
	m, err := OpenManager(t.TempDir(), t.TempDir(), registry)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := m.Install(context.Background(), fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Entry("pkg-sample"); ok {
		t.Fatal("install activated code")
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: installed.Revision, Expected: installed.Revision}); err == nil {
		t.Fatal("activation skipped confirmation")
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: installed.Revision, Expected: installed.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	old, _ := registry.Entry("pkg-sample")
	source := fixture.Source.Location
	var manifest Manifest
	raw, _ := os.ReadFile(filepath.Join(source, ManifestName))
	json.Unmarshal(raw, &manifest)
	manifest.Version = "2.0.0"
	raw, _ = json.Marshal(manifest)
	os.WriteFile(filepath.Join(source, ManifestName), raw, 0600)
	os.WriteFile(filepath.Join(source, "tool.py"), []byte("print('not protocol')"), 0600)
	candidate, err := m.Install(context.Background(), fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: candidate.Revision, Expected: installed.Revision, Confirm: true}); err == nil {
		t.Fatal("bad candidate activated")
	}
	current, _ := registry.Entry("pkg-sample")
	if current.Plugin != old.Plugin || current.State != plugin.StateEnabled {
		t.Fatal("failed upgrade replaced old capability")
	}
	entries := m.List()
	if len(entries) != 1 || entries[0].Current != installed.Revision || !entries[0].Enabled {
		t.Fatalf("catalog changed on failure: %+v", entries)
	}
	if err = m.SetEnabled("sample", installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	current, _ = registry.Entry("pkg-sample")
	if current.State != plugin.StateDisabled {
		t.Fatal("disable did not affect registry")
	}
	reopened, err := OpenManager(m.store.dir, m.stateRoot, packageRegistry())
	if err != nil || reopened.List()[0].Enabled {
		t.Fatal("disabled state did not persist", err)
	}
}
func TestManagerRollbackAndRemovalKeepUserState(t *testing.T) {
	requirePackageSandbox(t)
	_, fixture := protocolPackage(t)
	registry := packageRegistry()
	m, _ := OpenManager(t.TempDir(), t.TempDir(), registry)
	a, err := m.Install(context.Background(), fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: a.Revision, Expected: a.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(fixture.Source.Location, "tool.py"))
	os.WriteFile(filepath.Join(fixture.Source.Location, "tool.py"), append(body, []byte("\n# revision two\n")...), 0600)
	b, err := m.Install(context.Background(), fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: b.Revision, Expected: a.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: a.Revision, Expected: b.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(m.stateRoot, "pkg-sample")
	os.MkdirAll(dataDir, 0700)
	os.WriteFile(filepath.Join(dataDir, "user.txt"), []byte("keep"), 0600)
	if err = m.Remove("sample", a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Entry("pkg-sample"); ok {
		t.Fatal("removed package still registered")
	}
	if _, err = os.Stat(filepath.Join(dataDir, "user.txt")); err != nil {
		t.Fatal("removal deleted user state")
	}
	if _, err = m.store.Version("sample", b.Revision); err != nil {
		t.Fatal("removal deleted rollback revision")
	}
}

func TestTamperedPackageCanStillBeDisabled(t *testing.T) {
	requirePackageSandbox(t)
	_, fixture := protocolPackage(t)
	r := packageRegistry()
	m, _ := OpenManager(t.TempDir(), t.TempDir(), r)
	v, err := m.Install(context.Background(), fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Activate(context.Background(), Activation{ID: "sample", Revision: v.Revision, Expected: v.Revision, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(v.Root, "tool.py"), []byte("tampered"), 0600)
	if err = m.SetEnabled("sample", v.Revision, false); err != nil {
		t.Fatal("tampered package cannot be disabled", err)
	}
	entry, _ := r.Entry("pkg-sample")
	if entry.State != plugin.StateDisabled {
		t.Fatal("package stayed enabled")
	}
}
