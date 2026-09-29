package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPrebuiltRunsAndReloadsWithoutGoOrWritableInstallation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	install, cache := t.TempDir(), t.TempDir()
	binary := filepath.Join(install, "text-transform")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./plugins/text_transform")
	cmd.Dir = testRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	specs := []ToolSpec{Allowlist[0]}
	binaries := map[string]PrebuiltBinary{ToolTextTransform: {Path: binary, SHA256: hex.EncodeToString(hash[:])}}
	t.Setenv("PATH", "/unavailable-go")
	h, err := New(ctx, install, Options{Tools: specs, Prebuilt: binaries, RuntimeDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	if err := h.Reload(ctx, ToolTextTransform); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(install, ".runtime")); !os.IsNotExist(err) {
		t.Fatal("wrote runtime into installation")
	}
	// 外部修改Options不能改变已登记来源；重载每次重新验证，而不是静默构建。
	binaries[ToolTextTransform] = PrebuiltBinary{Path: "/missing"}
	if err := h.Reload(ctx, ToolTextTransform); err != nil {
		t.Fatal("caller mutated host options", err)
	}
	before := h.State().Active(ToolTextTransform)
	if err := os.WriteFile(binary, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(ctx, ToolTextTransform); err == nil {
		t.Fatal("tampered binary reloaded")
	}
	if h.State().Active(ToolTextTransform).Generation != before.Generation {
		t.Fatal("failed reload discarded old generation")
	}
	h.Close()
	if _, err := os.Stat(binary); err != nil {
		t.Fatal("close removed installation binary", err)
	}
	if files, err := os.ReadDir(cache); err != nil || len(files) != 0 {
		t.Fatal("temporary executable not cleaned", err)
	}
}

func TestPrebuiltRequiresCompleteExplicitMapping(t *testing.T) {
	_, err := New(context.Background(), t.TempDir(), Options{Prebuilt: map[string]PrebuiltBinary{}, RuntimeDir: t.TempDir()})
	if err == nil {
		t.Fatal("incomplete release silently fell back to building")
	}
}
