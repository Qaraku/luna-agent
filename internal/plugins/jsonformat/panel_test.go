package jsonformat

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPanelModule(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", "internal/plugins/jsonformat/panel.test.mjs")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("panel tests: %v\n%s", err, out)
	}
}
