package main

import (
	"bytes"
	"encoding/json"
	"github.com/Qaraku/luna-agent/internal/plugins/memory"
	"github.com/Qaraku/luna-agent/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/datalifecycle"
	"github.com/Qaraku/luna-agent/internal/layout"
	"github.com/Qaraku/luna-agent/internal/privatebackup"
)

func privatePlanFixture(t *testing.T) (privatebackup.Plan, string, string) {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "data")
	repo := filepath.Join(base, "old-repo")
	configDir := filepath.Join(base, "config")
	for _, dir := range []string{data, repo, configDir, filepath.Join(repo, ".runtime/sessions"), filepath.Join(repo, "presets"), filepath.Join(data, "skills")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{filepath.Join(repo, ".runtime/memory.jsonl"): "MEMORY_FIXTURE", filepath.Join(repo, ".runtime/sessions/session.jsonl"): "SESSION_FIXTURE", filepath.Join(repo, ".runtime/luna"): "DO_NOT_COPY_BINARY", filepath.Join(repo, "source.go"): "DO_NOT_COPY_REPO", filepath.Join(configDir, "provider.yaml"): "CREDENTIAL_FIXTURE"} {
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := makeDataPlan(layout.Paths{Config: configDir, Data: data}, filepath.Join(repo, ".runtime/sessions"), repo, filepath.Join(configDir, "config.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	file := filepath.Join(base, "plan.json")
	os.WriteFile(file, raw, 0600)
	return p, file, base
}
func TestDataCommandsBackUpLegacyFilesWithoutCopyingInstallation(t *testing.T) {
	_, plan, base := privatePlanFixture(t)
	archive := filepath.Join(base, "private.tar.gz")
	var out bytes.Buffer
	if err := dataCommand([]string{"backup", "-plan", plan, "-out", archive, "-offline", "-include-private"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "CREDENTIAL_FIXTURE") || strings.Contains(out.String(), "MEMORY_FIXTURE") {
		t.Fatal("printed private data")
	}
	dest := filepath.Join(base, "restored")
	if err := dataCommand([]string{"restore", "-archive", archive, "-dest", dest, "-include-private"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"provider.yaml", ".runtime/memory.jsonl", "sessions/session.jsonl"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatal("missing restored data", name, err)
		}
	}
	for _, name := range []string{"source.go", ".runtime/luna"} {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Fatal("copied runtime/repository content", name)
		}
	}
}
func TestDataBackupRequiresExplicitOfflineAndPrivateConfirmation(t *testing.T) {
	_, plan, base := privatePlanFixture(t)
	for _, extra := range [][]string{nil, {"-offline"}, {"-include-private"}} {
		archive := filepath.Join(base, "output.tar.gz")
		args := append([]string{"backup", "-plan", plan, "-out", archive}, extra...)
		if err := dataCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatal("backup ran without both confirmations")
		}
		if _, err := os.Stat(archive); !os.IsNotExist(err) {
			t.Fatal("unconfirmed backup was written")
		}
	}
}
func TestDataBackupRefusesLiveDataGuard(t *testing.T) {
	p, plan, base := privatePlanFixture(t)
	guard, err := datalifecycle.Acquire(p.LockPaths())
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	archive := filepath.Join(base, "private.tar.gz")
	if err := dataCommand([]string{"backup", "-plan", plan, "-out", archive, "-offline", "-include-private"}, &bytes.Buffer{}); err == nil {
		t.Fatal("live data backed up")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("online backup created output")
	}
}

func TestRestoredSessionAndMemoryOpenWithProductionReaders(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	p, err := makeDataPlan(layout.Paths{Config: home, Data: home}, filepath.Join(home, "sessions"), home, filepath.Join(home, "config.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Open(p.SessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := sessions.Create("可恢复会话")
	if err != nil {
		t.Fatal(err)
	}
	if err = sessions.AppendMessage(id, store.MessageRecord{Role: store.RoleUser, Text: "会话内容夹具", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	memories, err := memory.Open(filepath.Join(home, ".runtime", memory.StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = memories.Remember(id, "偏好简体中文", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(base, "plan.json")
	raw, _ := json.Marshal(p)
	os.WriteFile(planFile, raw, 0600)
	archive := filepath.Join(base, "private.tar.gz")
	if err = dataCommand([]string{"backup", "-plan", planFile, "-out", archive, "-offline", "-include-private"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(base, "restored")
	if err = dataCommand([]string{"restore", "-archive", archive, "-dest", dest, "-include-private"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(filepath.Join(dest, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := restored.Messages(id)
	if err != nil || len(messages) != 1 || messages[0].Text != "会话内容夹具" {
		t.Fatal("session could not be reopened", messages, err)
	}
	restoredMemory, err := memory.Open(filepath.Join(dest, ".runtime", memory.StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := restoredMemory.Facts()
	if err != nil || len(facts) != 1 || facts[0].Text != "偏好简体中文" || facts[0].SourceSession != id {
		t.Fatal("memory could not be reopened", facts, err)
	}
}
