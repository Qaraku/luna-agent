package packages

import (
	"context"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRetainsRemovedPackageStateWithoutActivation(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	registry := plugin.NewRegistry(plugin.PermissionStateWrite, plugin.PermissionProcessExec, plugin.PermissionFilesystemWrite, plugin.PermissionNetworkFetch)
	manager, err := OpenManager(dir, state, registry)
	if err != nil {
		t.Fatal(err)
	}
	version, err := manager.Install(context.Background(), Source{Kind: "local", Location: packageSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(version.Manifest.ID, version.Revision); err != nil {
		t.Fatal(err)
	}
	names, err := RetainedStateNamespaces(dir)
	if err != nil || len(names) != 1 || names[0] != "pkg-sample" {
		t.Fatal(names, err)
	}
	if _, exists := registry.Entry("pkg-sample"); exists {
		t.Fatal("backup lookup activated a package")
	}
	if err := os.WriteFile(filepath.Join(dir, catalogName), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RetainedStateNamespaces(dir); err == nil {
		t.Fatal("corrupt catalog silently omitted package states")
	}
}
