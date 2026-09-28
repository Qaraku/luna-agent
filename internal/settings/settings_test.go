package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "luna", FileName)
}

func TestAMissingFileIsNotAnError(t *testing.T) {
	file, found, err := Load(settingsPath(t))
	if err != nil {
		t.Fatalf("a missing settings file is the normal first run: %v", err)
	}
	if found {
		t.Fatal("found reported true for a file that is not there")
	}
	if len(file.DisabledSkills()) != 0 {
		t.Fatalf("disabled=%v, want none", file.DisabledSkills())
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := settingsPath(t)
	want := Settings{Skills: Skills{Disabled: []string{"alpha", "beta"}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the file was just written but Load reports it is not there")
	}
	if names := got.DisabledSkills(); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("disabled=%v, want [alpha beta]", names)
	}
}

func TestTheWrittenFileHasTheDocumentedShape(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Skills: Skills{Disabled: []string{"alpha"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "skills:\n  disabled:\n    - alpha\n"
	if string(data) != want {
		t.Fatalf("file=%q, want %q", data, want)
	}
	// Settings with nothing turned off say nothing, rather than an empty list
	// that the next reader has to interpret.
	if err := Save(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "disabled") {
		t.Fatalf("file=%q, want no disabled key when nothing is turned off", data)
	}
}

func TestTheRewriteLeavesNoTemporaryFile(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Skills: Skills{Disabled: []string{"alpha"}}}); err != nil {
		t.Fatal(err)
	}
	// A second write is the case that would leave a temporary file behind if
	// the rename were skipped or the file were closed after it.
	if err := Save(path, Settings{Skills: Skills{Disabled: []string{"beta"}}}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory holds %v, want only %s", names, FileName)
	}
	file, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if names := file.DisabledSkills(); len(names) != 1 || names[0] != "beta" {
		t.Fatalf("disabled=%v, want only the second write's entry", names)
	}
}

func TestTheDirectoryAndFileArePrivate(t *testing.T) {
	path := settingsPath(t)
	dir := filepath.Dir(path)
	if err := Save(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("directory mode=%o, want 700", mode)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode=%o, want 600", mode)
	}
}

func TestAnUnknownKeyIsRefused(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("skills:\n  disabled:\n    - alpha\nskils:\n  disabled: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := Load(path)
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if found {
		t.Fatal("a file that failed to load reported itself as found")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Fatalf("err=%v, want it to name the file", err)
	}
	if strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("err=%v, want no host path", err)
	}
}

func TestAReadFailureNamesTheFileOnly(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file is expected: readable name, unreadable
	// content. The message must name the file and nothing about where it
	// lives on this host.
	path := filepath.Join(dir, FileName)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("reading a directory as the settings file was accepted")
	}
	if !strings.Contains(err.Error(), FileName) || strings.Contains(err.Error(), dir) {
		t.Fatalf("err=%v, want the file name and no host path", err)
	}
}

func TestBlankAndRepeatedNamesAreDropped(t *testing.T) {
	path := settingsPath(t)
	file := Settings{Skills: Skills{Disabled: []string{" alpha ", "", "alpha", "beta", "beta"}}}
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if names := got.DisabledSkills(); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("disabled=%v, want [alpha beta] once each", names)
	}
}

func TestTurningOneSkillOffAndOn(t *testing.T) {
	file := Settings{Skills: Skills{Disabled: []string{"alpha", "beta"}}}
	off := file.WithSkillDisabled("gamma", true)
	if names := off.DisabledSkills(); len(names) != 3 || names[2] != "gamma" {
		t.Fatalf("disabled=%v, want gamma appended", names)
	}
	back := off.WithSkillDisabled("alpha", false)
	if names := back.DisabledSkills(); len(names) != 2 || names[0] != "beta" || names[1] != "gamma" {
		t.Fatalf("disabled=%v, want alpha gone and the rest in place", names)
	}
	// Turning off what is already off changes nothing, and an empty name is not
	// a skill.
	if names := back.WithSkillDisabled("beta", true).DisabledSkills(); len(names) != 2 {
		t.Fatalf("disabled=%v, want no repeat", names)
	}
	if names := back.WithSkillDisabled("  ", true).DisabledSkills(); len(names) != 2 {
		t.Fatalf("disabled=%v, want a blank name ignored", names)
	}
}

func TestAnEmptyFileIsAnEmptySetting(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, found, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(file.DisabledSkills()) != 0 || len(file.EnabledCapabilities()) != 0 {
		t.Fatalf("found=%v disabled=%v enabled=%v, want an empty setting", found, file.DisabledSkills(), file.EnabledCapabilities())
	}
}

func TestEnabledCapabilitiesRoundTrip(t *testing.T) {
	path := settingsPath(t)
	want := Settings{Capabilities: Capabilities{Enabled: []string{"terminal", "network"}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the file was just written but Load reports it is not there")
	}
	if names := got.EnabledCapabilities(); len(names) != 2 || names[0] != "terminal" || names[1] != "network" {
		t.Fatalf("enabled=%v, want [terminal network]", names)
	}
	// Nothing is enabled by default: an empty list is the normal state, and
	// absence in the file means the same thing.
	if names := (Settings{}).EnabledCapabilities(); len(names) != 0 {
		t.Fatalf("enabled=%v on empty settings, want none", names)
	}
}

func TestTheCapabilitiesKeyHasTheDocumentedShape(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Capabilities: Capabilities{Enabled: []string{"terminal"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "capabilities:\n  enabled:\n    - terminal\n"
	if string(data) != want {
		t.Fatalf("file=%q, want %q", data, want)
	}
	// Nothing enabled says nothing, the same way nothing turned off does: a
	// section nobody has touched is not worth a line in the file.
	if err := Save(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "capabilities") || strings.Contains(string(data), "skills") {
		t.Fatalf("file=%q, want no section when nothing was chosen", data)
	}
}

// The two choices share one file, so each write has to carry the other one
// through untouched. A skill toggle that dropped the enabled capabilities (or
// the other way round) would look like the user's choice was forgotten.
func TestSkillsAndCapabilitiesAreWrittenTogether(t *testing.T) {
	path := settingsPath(t)
	file := Settings{
		Skills:       Skills{Disabled: []string{"alpha"}},
		Capabilities: Capabilities{Enabled: []string{"terminal"}},
	}
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "skills:\n  disabled:\n    - alpha\ncapabilities:\n  enabled:\n    - terminal\n"
	if string(data) != want {
		t.Fatalf("file=%q, want %q", data, want)
	}

	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	afterSkill := loaded.WithSkillDisabled("beta", true)
	if names := afterSkill.EnabledCapabilities(); len(names) != 1 || names[0] != "terminal" {
		t.Fatalf("enabled=%v, want terminal kept across a skill change", names)
	}
	afterCapability := loaded.WithCapabilityEnabled("network", true)
	if names := afterCapability.DisabledSkills(); len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("disabled=%v, want alpha kept across a capability change", names)
	}
	// Both changes together, through the file: neither list may lose an entry
	// to the other's rewrite.
	next := afterSkill.WithCapabilityEnabled("network", true)
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	back, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if names := back.DisabledSkills(); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("disabled=%v, want [alpha beta]", names)
	}
	if names := back.EnabledCapabilities(); len(names) != 2 || names[0] != "terminal" || names[1] != "network" {
		t.Fatalf("enabled=%v, want [terminal network]", names)
	}
}

func TestTurningOneCapabilityOnAndOff(t *testing.T) {
	file := Settings{Capabilities: Capabilities{Enabled: []string{"terminal", "network"}}}
	withExtra := file.WithCapabilityEnabled("shell", true)
	if names := withExtra.EnabledCapabilities(); len(names) != 3 || names[2] != "shell" {
		t.Fatalf("enabled=%v, want shell appended", names)
	}
	back := withExtra.WithCapabilityEnabled("terminal", false)
	if names := back.EnabledCapabilities(); len(names) != 2 || names[0] != "network" || names[1] != "shell" {
		t.Fatalf("enabled=%v, want terminal gone and the rest in place", names)
	}
	// Turning on what is already on changes nothing, and an empty name is not
	// a capability.
	if names := back.WithCapabilityEnabled("network", true).EnabledCapabilities(); len(names) != 2 {
		t.Fatalf("enabled=%v, want no repeat", names)
	}
	if names := back.WithCapabilityEnabled("  ", true).EnabledCapabilities(); len(names) != 2 {
		t.Fatalf("enabled=%v, want a blank name ignored", names)
	}
	// Blanks and repeats in the file are normalized on the way out.
	path := settingsPath(t)
	messy := Settings{Capabilities: Capabilities{Enabled: []string{" terminal ", "", "terminal", "network", "network"}}}
	if err := Save(path, messy); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if names := got.EnabledCapabilities(); len(names) != 2 || names[0] != "terminal" || names[1] != "network" {
		t.Fatalf("enabled=%v, want [terminal network] once each", names)
	}
}

func TestAnUnknownCapabilityKeyIsRefused(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("capabilities:\n  enabled:\n    - terminal\n  enabld: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := Load(path)
	if err == nil {
		t.Fatal("a misspelled key under capabilities was accepted")
	}
	if found {
		t.Fatal("a file that failed to load reported itself as found")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Fatalf("err=%v, want it to name the file", err)
	}
	if strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("err=%v, want no host path", err)
	}
}

// The default is the whole point of the write section: a capability that changes
// files on someone's machine is one they have to have asked for. Nothing in the
// file, and no directory anywhere.
func TestNothingMayBeWrittenUntilTheUserSaysSo(t *testing.T) {
	if dirs := (Settings{}).WriteDirs(); len(dirs) != 0 {
		t.Fatalf("dirs=%v on empty settings, want none", dirs)
	}
	file, found, err := Load(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if found || len(file.WriteDirs()) != 0 {
		t.Fatalf("found=%v dirs=%v, want no file and no grant", found, file.WriteDirs())
	}
	// A file nothing was chosen in says nothing about writing either: an empty
	// list would read as "the user decided about writing", which they did not.
	path := settingsPath(t)
	if err := Save(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "write") {
		t.Fatalf("file=%q, want no write key when nothing was allowed", data)
	}
}

func TestAllowingAndDisallowingADirectory(t *testing.T) {
	first := filepath.Join(t.TempDir(), "project")
	second := filepath.Join(t.TempDir(), "notes")
	file := (Settings{}).WithWriteDir(first, true).WithWriteDir(second, true)
	if dirs := file.WriteDirs(); len(dirs) != 2 || dirs[0] != first || dirs[1] != second {
		t.Fatalf("dirs=%v, want [%s %s]", dirs, first, second)
	}
	// One directory taken away leaves the other where it was, and allowing the
	// same one twice writes it once.
	back := file.WithWriteDir(first, false)
	if dirs := back.WriteDirs(); len(dirs) != 1 || dirs[0] != second {
		t.Fatalf("dirs=%v, want only %s", dirs, second)
	}
	if dirs := back.WithWriteDir(second, true).WriteDirs(); len(dirs) != 1 {
		t.Fatalf("dirs=%v, want no repeat", dirs)
	}
	// A blank entry, and one that is not an absolute path, are not directories
	// the user could have allowed.
	if dirs := back.WithWriteDir("  ", true).WriteDirs(); len(dirs) != 1 {
		t.Fatalf("dirs=%v, want a blank entry ignored", dirs)
	}
	if dirs := back.WithWriteDir("some/where", true).WriteDirs(); len(dirs) != 1 {
		t.Fatalf("dirs=%v, want a relative entry ignored", dirs)
	}
}

func TestTheWriteSectionHasTheDocumentedShape(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Write: Write{Dirs: []string{"/srv/project"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "write:\n  dirs:\n    - /srv/project\n"
	if string(data) != want {
		t.Fatalf("file=%q, want %q", data, want)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirs := got.WriteDirs(); len(dirs) != 1 || dirs[0] != "/srv/project" {
		t.Fatalf("dirs=%v, want [/srv/project]", dirs)
	}
}

// The grant is compared against a resolved path, so two spellings of one
// directory are one grant, and an entry that could not name a directory is not
// carried along as if it were one.
func TestAPathIsCleanedSoOneDirectoryIsOneGrant(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	hand := "write:\n  dirs:\n    - /srv/project/\n    - /srv/./project\n    - relative/path\n    - \"\"\n"
	if err := os.WriteFile(path, []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	file, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirs := file.WriteDirs(); len(dirs) != 1 || dirs[0] != "/srv/project" {
		t.Fatalf("dirs=%v, want [/srv/project] once", dirs)
	}
	// What is read is what is written back: the entries that are not grants do
	// not survive a save, rather than sitting in the file looking like grants.
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "write:\n  dirs:\n    - /srv/project\n"; string(data) != want {
		t.Fatalf("file=%q, want %q", data, want)
	}
}

func TestAnUnknownKeyUnderWriteIsRefused(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("write:\n  dirs:\n    - /srv/project\n  allow_all: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := Load(path)
	if err == nil {
		t.Fatal("a key nothing expects under write was accepted")
	}
	if found {
		t.Fatal("a file that failed to load reported itself as found")
	}
	if !strings.Contains(err.Error(), FileName) || strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("err=%v, want the file name and no host path", err)
	}
}

// A capability the product does not run on its own is off unless the user's list
// names it, so the answer is a question about the list rather than about whether a
// key is there.
func TestACapabilityIsOffUnlessTheListNamesIt(t *testing.T) {
	file := Settings{Capabilities: Capabilities{Enabled: []string{"terminal"}}}
	if !file.CapabilityEnabled("terminal") {
		t.Fatal("terminal is in the list and was reported as off")
	}
	if file.CapabilityEnabled("memory") {
		t.Fatal("a capability that is not in the list was reported as on")
	}
	if (Settings{}).CapabilityEnabled("terminal") {
		t.Fatal("empty settings turned a capability on")
	}
}

// All three choices share one file, so each write has to carry the other two
// through untouched. A toggle that dropped the write grant — or the grant dropped a
// disabled skill — would look like the user's choice was forgotten.
func TestTheThreeChoicesAreWrittenTogether(t *testing.T) {
	path := settingsPath(t)
	granted := filepath.Join(t.TempDir(), "project")
	file := Settings{
		Skills:       Skills{Disabled: []string{"alpha"}},
		Capabilities: Capabilities{Enabled: []string{"terminal"}},
	}.WithWriteDir(granted, true)
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirs := loaded.WithSkillDisabled("beta", true).WriteDirs(); len(dirs) != 1 || dirs[0] != granted {
		t.Fatalf("dirs=%v, want the grant kept across a skill change", dirs)
	}
	if dirs := loaded.WithCapabilityEnabled("network", true).WriteDirs(); len(dirs) != 1 || dirs[0] != granted {
		t.Fatalf("dirs=%v, want the grant kept across a capability change", dirs)
	}
	afterGrant := loaded.WithWriteDir(granted, false)
	if names := afterGrant.DisabledSkills(); len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("disabled=%v, want alpha kept across a grant change", names)
	}
	if names := afterGrant.EnabledCapabilities(); len(names) != 1 || names[0] != "terminal" {
		t.Fatalf("enabled=%v, want terminal kept across a grant change", names)
	}
}
