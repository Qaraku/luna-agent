package packages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/plugins/terminal"
)

func requirePackageSandbox(t *testing.T) {
	t.Helper()
	_, err := terminal.ExecuteIsolated(context.Background(), terminal.IsolatedRequest{Command: "true"})
	if err != nil {
		if os.Getenv("LUNA_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
}
func protocolPackage(t *testing.T) (*Store, Version) {
	t.Helper()
	source := packageSource(t)
	m := sampleManifest()
	m.Tools[0].Input.Properties = map[string]*Schema{"text": {Type: "string"}, "path": {Type: "string"}}
	raw, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(source, ManifestName), raw, 0600)
	os.WriteFile(filepath.Join(source, "tool.py"), []byte("import json,sys,os\nr=json.load(sys.stdin)\nif r['method']=='describe':\n print(json.dumps({'protocol':1,'tools':['echo']}))\nelse:\n a=r['arguments']\n try: v=open(a['path']).read() if 'path' in a else a['text']+'|'+os.environ.get('OPENAI_API_KEY','missing')\n except OSError: v='DENIED'\n print(json.dumps({'protocol':1,'result':v}))\n"), 0600)
	store, _ := OpenStore(t.TempDir())
	v, err := store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	return store, v
}
func TestPackagedToolUsesProtocolAndChecksPermissionsBeforeExecution(t *testing.T) {
	requirePackageSandbox(t)
	store, version := protocolPackage(t)
	cap, err := NewCapability(store, version, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cap.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	tool := cap.Tools()[0]
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run"})
	if _, err = tool.Invoke(ctx, `{"text":"hello"}`); !errors.Is(err, plugin.ErrApprovalRequired) {
		t.Fatalf("execution did not ask: %v", err)
	}
	approvals := 0
	ctx = plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run", Approve: func(context.Context, plugin.AccessRequest) error { approvals++; return nil }})
	t.Setenv("OPENAI_API_KEY", "private-test-value")
	output, err := tool.Invoke(ctx, `{"text":"hello"}`)
	if err != nil || output != "hello|missing" || approvals != 1 {
		t.Fatalf("output=%q approvals=%d err=%v", output, approvals, err)
	}
	if _, err = tool.Invoke(ctx, `{"text":"hello","grant":"all"}`); err == nil || approvals != 1 {
		t.Fatal("invalid arguments reached approval/execution")
	}
}
func TestPackagedToolWithoutReadCannotReadProjectOrHostFiles(t *testing.T) {
	requirePackageSandbox(t)
	store, v := protocolPackage(t)
	cap, err := NewCapability(store, v, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "private.txt")
	os.WriteFile(path, []byte("not exposed"), 0600)
	ctx := plugin.WithRoots(plugin.WithRun(context.Background(), plugin.RunInfo{SessionID: "aaaaaaaa", RunID: "run", Approve: func(context.Context, plugin.AccessRequest) error { return nil }}), []string{root})
	args, _ := json.Marshal(map[string]string{"text": "x", "path": path})
	out, err := cap.Tools()[0].Invoke(ctx, string(args))
	if err != nil || out != "DENIED" {
		t.Fatalf("undeclared read=%q %v", out, err)
	}
}

func TestPackageStateWritesRequirePrivateScopeApproval(t *testing.T) {
	requirePackageSandbox(t)
	source := packageSource(t)
	manifest := sampleManifest()
	manifest.Tools[0].Access.State = "write"
	raw, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(source, ManifestName), raw, 0600)
	os.WriteFile(filepath.Join(source, "tool.py"), []byte("import json,sys,pathlib\nr=json.load(sys.stdin)\nif r['method']=='describe': print(json.dumps({'protocol':1,'tools':['echo']}))\nelse:\n pathlib.Path(r['state_dir'],'saved.txt').write_text(r['arguments']['text'])\n print(json.dumps({'protocol':1,'result':'saved'}))\n"), 0600)
	store, _ := OpenStore(t.TempDir())
	version, err := store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	cap, err := NewCapability(store, version, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := plugin.AccessPolicy{Read: plugin.DecisionAllow, Write: plugin.DecisionAllow, Exec: plugin.DecisionAllow, Network: plugin.DecisionDeny}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Permissions: &policy, Approve: func(_ context.Context, r plugin.AccessRequest) error {
		if !r.ScopeApproval || len(r.WriteRoots) != 1 || r.WriteRoots[0] != state {
			t.Fatal("private state scope not shown")
		}
		if _, err := os.Stat(filepath.Join(state, "saved.txt")); !os.IsNotExist(err) {
			t.Fatal("state was written before approval")
		}
		return nil
	}})
	out, err := cap.Tools()[0].Invoke(ctx, `{"text":"kept"}`)
	if err != nil || out != "saved" {
		t.Fatalf("state tool=%q %v", out, err)
	}
	data, _ := os.ReadFile(filepath.Join(state, "saved.txt"))
	if string(data) != "kept" {
		t.Fatal("state output missing")
	}
}
func TestShippedWritingPackageIsUsableWithoutProjectAccess(t *testing.T) {
	requirePackageSandbox(t)
	source, err := filepath.Abs("../../../plugins/packages/writing-helper")
	if err != nil {
		t.Fatal(err)
	}
	store, _ := OpenStore(t.TempDir())
	v, err := store.Stage(context.Background(), Source{Kind: "local", Location: source})
	if err != nil {
		t.Fatal(err)
	}
	cap, err := NewCapability(store, v, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cap.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Approve: func(context.Context, plugin.AccessRequest) error { return nil }})
	out, err := cap.Tools()[0].Invoke(ctx, `{"text":"hello world"}`)
	if err != nil || !strings.Contains(out, `"whitespace_words": 2`) {
		t.Fatalf("sample result=%s %v", out, err)
	}
}

func TestPackageCodeChangedDuringApprovalIsNotExecuted(t *testing.T) {
	requirePackageSandbox(t)
	store, v := protocolPackage(t)
	cap, err := NewCapability(store, v, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := plugin.WithRun(context.Background(), plugin.RunInfo{Approve: func(context.Context, plugin.AccessRequest) error {
		return os.WriteFile(filepath.Join(v.Root, "tool.py"), []byte("print('{\"protocol\":1,\"result\":\"tampered\"}')"), 0600)
	}})
	if output, err := cap.Tools()[0].Invoke(ctx, `{"text":"hello"}`); err == nil {
		t.Fatalf("changed code executed: %q", output)
	}
}
