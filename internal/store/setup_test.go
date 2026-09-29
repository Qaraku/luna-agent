package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Qaraku/luna-agent/internal/runconfig"
)

func TestRunSetupAndReceiptSurviveSessionDecode(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Create("setup")
	if err != nil {
		t.Fatal(err)
	}
	setup := &runconfig.Selection{Owner: "presets", ID: "private", Revision: "rev", Instructions: "work", Capabilities: []string{}, Resources: map[string][]string{"catalog": {}}}
	if err = s.AppendConfig(id, ConfigRecord{Model: "one", Setup: setup}); err != nil {
		t.Fatal(err)
	}
	receipt := &runconfig.Snapshot{Selection: setup, Model: "one", Capabilities: []string{}, Tools: []string{"read"}, Resources: map[string][]string{"catalog": {}}}
	if err = s.AppendRun(id, RunRecord{RunID: "run", Status: StatusOK, Configuration: receipt}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir(), id+FileSuffix)
	before, _ := os.ReadFile(path)
	got, err := s.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config == nil || got.Config.Setup == nil || got.Config.Setup.Revision != "rev" || got.Config.Setup.Capabilities == nil {
		t.Fatalf("selection lost: %+v", got.Config)
	}
	last := got.Records[len(got.Records)-1].Run
	if last == nil || last.Configuration == nil || last.Configuration.Selection.Revision != "rev" || len(last.Configuration.Tools) != 1 {
		t.Fatalf("receipt lost: %+v", last)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("reading rewrote records")
	}
}
