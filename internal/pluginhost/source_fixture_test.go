package pluginhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 测试更换的是隔离副本中的实际源码，再调用正常重载；生产宿主不认识这些夹具名。
func prepareBuildRoot(t *testing.T, h *Host) {
	t.Helper()
	original, err := filepath.EvalSymlinks(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if h.root != original {
		return
	}
	root := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(original, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 依赖的是当前正式实现；只复制待修改的插件源码，不复制缓存或用户数据。
	if err := os.Symlink(filepath.Join(original, "internal"), filepath.Join(root, "internal")); err != nil {
		t.Fatal(err)
	}
	for _, spec := range Allowlist {
		dir := filepath.Join(root, "plugins", spec.Dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(original, "plugins", spec.Dir, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h.root = root
	t.Cleanup(h.Close)
}

func reloadFixture(t *testing.T, h *Host, fixture string) error {
	t.Helper()
	prepareBuildRoot(t, h)
	for _, spec := range Allowlist {
		source := filepath.Join(testRoot(t), "plugins", spec.Dir, "main.go")
		if fixture != "baseline" {
			source = filepath.Join(testRoot(t), "internal", "pluginhost", "testdata", fixture, spec.Dir, "main.go")
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.root, "plugins", spec.Dir, "main.go"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return h.Reload(context.Background(), "")
}
