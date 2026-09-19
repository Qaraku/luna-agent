package main

import "testing"

func TestRootFromExecutableRuntimeBinary(t *testing.T) {
	got := rootFromExecutable("/tmp/luna-agent/.runtime/luna")
	if got != "/tmp/luna-agent" {
		t.Fatalf("got %q", got)
	}
}
