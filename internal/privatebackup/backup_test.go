package privatebackup

import (
	"context"
	"github.com/Qaraku/luna-agent/internal/datalifecycle"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateSnapshotRoundTripPreservesContentsAndEmptyDirectories(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	os.MkdirAll(filepath.Join(source, "empty"), 0700)
	secret := []byte("SYNTHETIC_PRIVATE_FIXTURE")
	os.WriteFile(filepath.Join(source, "provider.yaml"), secret, 0600)
	os.WriteFile(filepath.Join(source, "method.sh"), []byte("#!/bin/sh\nprintf fixture\n"), 0700)
	sources := []Source{{ID: "data", Path: source, Target: "skills", Directory: true}, {ID: "missing", Path: filepath.Join(base, "absent"), Target: "config.yaml"}}
	archive := filepath.Join(base, "backup.tar.gz")
	m, err := Backup(context.Background(), sources, archive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 4 || m.Sources[1].Present {
		t.Fatal("missing/empty state lost", m.Sources, len(m.Entries))
	}
	info, _ := os.Stat(archive)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private archive mode", info.Mode())
	}
	if _, err := Inspect(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(base, "restore")
	if _, err := Restore(context.Background(), archive, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "skills/provider.yaml"))
	if err != nil || string(got) != string(secret) {
		t.Fatal("private file not restored", err)
	}
	info, err = os.Stat(filepath.Join(dest, "skills/method.sh"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("script mode not retained", err)
	}
	if _, err = os.Stat(filepath.Join(dest, "skills/empty")); err != nil {
		t.Fatal("empty directory lost")
	}
	if _, err = Restore(context.Background(), archive, dest); err == nil {
		t.Fatal("restored over existing data")
	}
	if _, err = Backup(context.Background(), sources, archive, nil); err == nil {
		t.Fatal("overwrote private backup")
	}
}
func TestBackupRejectsLinksOverlapAndOutputWithinSource(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	os.Mkdir(source, 0700)
	sources := []Source{{ID: "data", Path: source, Target: "skills", Directory: true}}
	if _, err := Backup(context.Background(), sources, filepath.Join(source, "out.tar.gz"), nil); err == nil {
		t.Fatal("backup wrote into its source")
	}
	os.Symlink("/etc/passwd", filepath.Join(source, "link"))
	archive := filepath.Join(base, "links.tar.gz")
	if _, err := Backup(context.Background(), sources, archive, nil); err == nil {
		t.Fatal("backup followed internal symlink")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("failed snapshot was published")
	}
	sources = append(sources, Source{ID: "nested", Path: source, Target: "skills/nested", Directory: true})
	if _, err := Backup(context.Background(), sources, archive, nil); err == nil {
		t.Fatal("overlapping restore targets accepted")
	}
}
func TestRestoreRejectsDamagedArchiveWithoutLeavingCandidate(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "note"), []byte(strings.Repeat("fixture", 512)), 0600)
	archive := filepath.Join(base, "backup.tar.gz")
	if _, err := Backup(context.Background(), []Source{{ID: "data", Path: source, Target: "skills", Directory: true}}, archive, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(archive)
	os.WriteFile(archive, raw[:len(raw)-12], 0600)
	dest := filepath.Join(base, "restored")
	if _, err := Restore(context.Background(), archive, dest); err == nil {
		t.Fatal("damaged archive accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("failed restore left a candidate")
	}
}
func TestBackupCancellationDoesNotPublish(t *testing.T) {
	base := t.TempDir()
	archive := filepath.Join(base, "out.tar.gz")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Backup(ctx, []Source{{ID: "data", Path: base, Target: "skills", Directory: true}}, archive, nil); err == nil {
		t.Fatal("cancelled snapshot succeeded")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("cancelled snapshot published")
	}
}

func TestRestoreRespectsDestinationDataGuard(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "new-home")
	guard, err := datalifecycle.Reserve([]string{dest})
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if _, err := Restore(context.Background(), filepath.Join(base, "unused.tar.gz"), dest); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatal("restore ignored active destination guard", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("restore touched reserved destination")
	}
}
