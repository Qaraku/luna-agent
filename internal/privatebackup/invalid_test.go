package privatebackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maliciousArchive(t *testing.T, m Manifest, h *tar.Header) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "test.tar.gz")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	raw, _ := json.Marshal(m)
	if err := tw.WriteHeader(&tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(raw))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(raw)
	if h != nil {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	file.Close()
	return name
}
func TestPrivateArchiveRejectsTraversalLinksAndFutureSchemas(t *testing.T) {
	base := Manifest{Format: 1, DataSchema: 1, CreatedAt: time.Now().UTC(), Sources: []SnapshotSource{{ID: "skills", Target: "skills", Directory: true, Present: true}}, Entries: []Entry{{Path: "skills", Directory: true}}}
	cases := []struct {
		name   string
		change func(*Manifest)
		header *tar.Header
	}{
		{name: "future", change: func(m *Manifest) { m.DataSchema = 999 }},
		{name: "traversal", change: func(m *Manifest) { m.Sources[0].Target = "../outside" }},
		{name: "link", header: &tar.Header{Name: "skills", Typeflag: tar.TypeSymlink, Linkname: "/etc"}},
		{name: "undeclared", header: &tar.Header{Name: "outside", Typeflag: tar.TypeDir, Mode: 0700}},
		{name: "missing", header: nil},
		{name: "extra-skill-escape", change: func(m *Manifest) { m.ExtraSkills = []string{"../outside"} }},
		{name: "entry-limit", change: func(m *Manifest) {
			m.Entries = append(m.Entries, Entry{Path: "skills/huge", Bytes: MaxFileBytes + 1, SHA256: strings.Repeat("0", 64)})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var m Manifest
			json.Unmarshal(raw, &m)
			if tc.change != nil {
				tc.change(&m)
			}
			archive := maliciousArchive(t, m, tc.header)
			dest := filepath.Join(t.TempDir(), "new")
			if _, err := Restore(context.Background(), archive, dest); err == nil {
				t.Fatal("accepted invalid backup")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("bad archive left destination")
			}
		})
	}
}
