package terminal

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestNetworkHelperBootstrap(t *testing.T) {
	requireSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	sandbox, err := prepareSandboxWithAccess(ctx, sandboxExecutable, []string{root}, root, "printf helper-ready", sandboxAccess{Read: true, Network: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.close()
	var output bytes.Buffer
	sandbox.cmd.Stdout = &output
	sandbox.cmd.Stderr = &output
	if err := sandbox.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sandbox.closeParentFiles()
	if err := sandbox.cmd.Wait(); err != nil {
		t.Fatalf("helper boot: %v; output=%s", err, output.String())
	}
	if !sandbox.ready() {
		t.Fatalf("no ready marker; output=%s", output.String())
	}
	if output.String() != "helper-ready" {
		t.Fatalf("output=%s", output.String())
	}
}
