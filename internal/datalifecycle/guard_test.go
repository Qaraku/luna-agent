package datalifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardSerializesCanonicalRootsAndKeepsMarkers(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "data")
	first, err := Acquire([]string{dir, dir})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("selected data root was not initialized", err)
	}
	second, err := Acquire([]string{dir})
	if err == nil {
		second.Close()
		t.Fatal("second process scope admitted")
	}
	first.Close()
	second, err = Acquire([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	second.Close()

	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	first, err = Acquire([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if other, err := Acquire([]string{alias}); err == nil {
		other.Close()
		t.Fatal("symlink bypassed data guard")
	}
}
func TestGuardRejectsNewerFormatWithoutOverwritingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	guard, err := Acquire([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	guard.Close()
	_, marker, err := sidecars(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"format":1,"data_schema":999}`)
	if err := os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if guard, err := Acquire([]string{dir}); err == nil {
		guard.Close()
		t.Fatal("newer format accepted")
	} else if !strings.Contains(err.Error(), "schema") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(marker)
	if string(after) != string(raw) {
		t.Fatal("format marker rewritten")
	}
}
func TestFailedMultiRootAcquisitionReleasesEarlierLocks(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	held, err := Acquire([]string{b})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if partial, err := Acquire([]string{a, b}); err == nil {
		partial.Close()
		t.Fatal("locked root admitted")
	}
	free, err := Acquire([]string{a})
	if err != nil {
		t.Fatal("failed acquisition leaked a lock", err)
	}
	free.Close()
}

func TestFormatMarkerFollowsCopiedDataAndDanglingRootsAreRejected(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "old")
	held, err := Acquire([]string{old})
	if err != nil {
		t.Fatal(err)
	}
	held.Close()
	_, marker, err := sidecars(old)
	if err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"format":1,"data_schema":999}`)
	if err = os.WriteFile(marker, newer, 0600); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(base, "copied")
	os.Mkdir(copied, 0700)
	if err = os.WriteFile(filepath.Join(copied, filepath.Base(marker)), newer, 0600); err != nil {
		t.Fatal(err)
	}
	if guard, err := Acquire([]string{copied}); err == nil {
		guard.Close()
		t.Fatal("copied future data marker lost its protection")
	}
	alias := filepath.Join(base, "broken")
	os.Symlink(filepath.Join(base, "missing"), alias)
	if _, err := Canonical(alias); err == nil {
		t.Fatal("dangling data root accepted")
	}
}

func TestIncompleteRestoreCannotBeOpenedAsNormalData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	guard, err := Acquire([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	guard.Close()
	if err := os.WriteFile(filepath.Join(dir, PendingRestoreName), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if guard, err := Acquire([]string{dir}); err == nil {
		guard.Close()
		t.Fatal("incomplete restore admitted as normal data")
	}
}
