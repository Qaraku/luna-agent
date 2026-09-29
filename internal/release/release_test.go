package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, Manifest) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"bin/luna": "binary fixture", "web/index.html": "web fixture", "plugins/bin/text_transform": "tool fixture"}
	m := Manifest{Format: 1, Version: "dev-fixture", Commit: strings.Repeat("a", 40), OS: runtime.GOOS, Arch: runtime.GOARCH, DataSchema: 1}
	for _, name := range []string{"bin/luna", "web/index.html", "plugins/bin/text_transform"} {
		data := []byte(files[name])
		hash := sha256.Sum256(data)
		ex := strings.HasPrefix(name, "bin/") || strings.HasPrefix(name, "plugins/bin/")
		m.Files = append(m.Files, File{Path: name, SHA256: hex.EncodeToString(hash[:]), Bytes: int64(len(data)), Executable: ex})
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
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dir, m
}
func TestPackInstallIsDeterministicAndNeverOverwrites(t *testing.T) {
	src, want := fixture(t)
	var a, b bytes.Buffer
	if err := Pack(context.Background(), src, &a); err != nil {
		t.Fatal(err)
	}
	if err := Pack(context.Background(), src, &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("archive not reproducible")
	}
	dest := filepath.Join(t.TempDir(), "installed")
	got, err := Install(context.Background(), bytes.NewReader(a.Bytes()), dest)
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != want.Commit {
		t.Fatal("lost source identity")
	}
	if _, err := Verify(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), bytes.NewReader(a.Bytes()), dest); err == nil {
		t.Fatal("overwrote installation")
	}
	if err := os.WriteFile(filepath.Join(src, "bin/luna"), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Pack(context.Background(), src, &bytes.Buffer{}); err == nil {
		t.Fatal("packed tampered binary")
	}
}
func TestInstallRejectsUnknownLinksTraversalAndPartialArchives(t *testing.T) {
	src, m := fixture(t)
	raw, _ := json.Marshal(m)
	for _, tc := range []struct {
		name   string
		header tar.Header
	}{{"link", tar.Header{Name: "bin/luna", Typeflag: tar.TypeSymlink, Linkname: "/tmp/outside"}}, {"escape", tar.Header{Name: "../outside", Typeflag: tar.TypeReg}}, {"unknown", tar.Header{Name: "config.yaml", Typeflag: tar.TypeReg}}, {"duplicate manifest", tar.Header{Name: ManifestName, Typeflag: tar.TypeReg}}} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			tw.WriteHeader(&tar.Header{Name: ManifestName, Size: int64(len(raw)), Mode: 0600})
			tw.Write(raw)
			tw.WriteHeader(&tc.header)
			tw.Close()
			gz.Close()
			dest := filepath.Join(t.TempDir(), "candidate")
			if _, err := Install(context.Background(), &buf, dest); err == nil {
				t.Fatal("accepted invalid archive")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("failed candidate left published")
			}
		})
	}
	var buf bytes.Buffer
	if err := Pack(context.Background(), src, &buf); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "truncated")
	if _, err := Install(context.Background(), bytes.NewReader(buf.Bytes()[:buf.Len()-12]), dest); err == nil {
		t.Fatal("accepted truncated gzip")
	}
}
func TestManifestRejectsIncompatibleDataAndPlatform(t *testing.T) {
	_, base := fixture(t)
	for _, change := range []func(*Manifest){func(m *Manifest) { m.DataSchema++ }, func(m *Manifest) { m.OS = "other" }, func(m *Manifest) { m.Files[0].Path = "../escape" }, func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) }, func(m *Manifest) { m.Commit = "main" }} {
		raw, _ := json.Marshal(base)
		var m Manifest
		json.Unmarshal(raw, &m)
		change(&m)
		if err := m.Validate(); err == nil {
			t.Fatal("accepted incompatible manifest")
		}
	}
}
