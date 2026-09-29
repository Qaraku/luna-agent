package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostIsolatedCommandHasBoundedInputAndReadonlyResources(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteIsolated(context.Background(), IsolatedRequest{Command: "cat; cat /run/luna/package/note.txt; printf changed > /run/luna/package/note.txt", Stdin: []byte("input\n"), Readonly: []ReadOnlyMount{{Source: root, Target: "/run/luna/package"}}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "input") || !strings.Contains(result.Stdout, "original") || result.ExitCode == 0 {
		t.Fatalf("result=%+v", result)
	}
	body, _ := os.ReadFile(filepath.Join(root, "note.txt"))
	if string(body) != "original" {
		t.Fatal("readonly package was modified")
	}
}
func TestHostMountCannotReplaceSecurityRuntime(t *testing.T) {
	_, err := ExecuteIsolated(context.Background(), IsolatedRequest{Command: "true", Readonly: []ReadOnlyMount{{Source: t.TempDir(), Target: "/usr"}}, Timeout: time.Second})
	if err == nil {
		t.Fatal("reserved mount target accepted")
	}
}
func TestHostResourceStaysReadonlyThroughProjectAlias(t *testing.T) {
	requireSandbox(t)
	root := t.TempDir()
	pkg := filepath.Join(root, "package")
	os.Mkdir(pkg, 0700)
	os.WriteFile(filepath.Join(pkg, "file"), []byte("kept"), 0600)
	result, err := ExecuteIsolated(context.Background(), IsolatedRequest{Command: "printf bad > " + shellQuote(filepath.Join(pkg, "file")), ReadRoots: []string{root}, WriteRoots: []string{root}, Readonly: []ReadOnlyMount{{Source: pkg, Target: "/run/luna/package"}}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 {
		t.Fatal("project mount made package writable")
	}
}
