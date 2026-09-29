package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/release"
)

func TestReleaseOptionsRequireCompleteVerifiedRegisteredTools(t *testing.T) {
	dir := t.TempDir()
	m := release.Manifest{Format: 1, Version: "dev-fixture", Commit: strings.Repeat("a", 40), OS: runtime.GOOS, Arch: runtime.GOARCH, DataSchema: 1}
	for _, name := range []string{"bin/luna", "web/index.html", "plugins/bin/text_transform"} {
		data := []byte(name)
		hash := sha256.Sum256(data)
		ex := !strings.HasPrefix(name, "web/")
		m.Files = append(m.Files, release.File{Path: name, SHA256: hex.EncodeToString(hash[:]), Bytes: int64(len(data)), Executable: ex})
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0600)
		if ex {
			mode = 0700
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, release.ManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	opts, dist, err := releaseOptions(context.Background(), dir, t.TempDir(), pluginhost.Allowlist[:1])
	if err != nil || !dist || opts.Prebuilt[pluginhost.ToolTextTransform].Path == "" {
		t.Fatal(opts, dist, err)
	}
	if _, _, err := releaseOptions(context.Background(), dir, t.TempDir(), pluginhost.Allowlist); err == nil {
		t.Fatal("release fell back to building missing registered tools")
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins/bin/text_transform"), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := releaseOptions(context.Background(), dir, t.TempDir(), pluginhost.Allowlist[:1]); err == nil {
		t.Fatal("tampered release selected")
	}
}
func TestSourceRootKeepsExistingBuildStrategy(t *testing.T) {
	opts, dist, err := releaseOptions(context.Background(), t.TempDir(), t.TempDir(), pluginhost.Allowlist)
	if err != nil || dist || opts.Prebuilt != nil {
		t.Fatal(opts, dist, err)
	}
}
