package packages

import (
	"encoding/json"
	"testing"
)

func sampleManifest() Manifest {
	return Manifest{Format: 1, ID: "sample", Version: "1.0.0", Title: "Sample", Files: []string{"tool.py"}, Tools: []ToolSpec{{Name: "echo", Description: "Echo", Entry: "tool.py", Interpreter: "python3", Input: Schema{Type: "object", Properties: map[string]*Schema{"text": {Type: "string"}}, Required: []string{"text"}}}}}
}
func TestPackageManifestRejectsEscapeSecretsAndUnboundedDeclarations(t *testing.T) {
	m := sampleManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/etc/passwd", ".env", "secrets/provider.yaml", ".git/config", "sessions/a.jsonl", "memory.jsonl", "dir/../tool.py"} {
		bad := sampleManifest()
		bad.Files = append(bad.Files, path)
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	bad := sampleManifest()
	bad.Tools[0].Access.Write = true
	if err := bad.Validate(); err == nil {
		t.Fatal("write without read accepted")
	}
	bad = sampleManifest()
	bad.Tools[0].Input = Schema{Type: "object", AdditionalProperties: true}
	if err := bad.Validate(); err == nil {
		t.Fatal("open parameter object accepted")
	}
}
func TestPackageSchemaActuallyRejectsUnknownAndInvalidParameters(t *testing.T) {
	m := sampleManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	schema := m.Tools[0].Input
	for _, args := range []string{`{"text":3}`, `{"text":"ok","permission":"allow"}`, `{}`, `null`} {
		if err := schema.ValidateJSON(json.RawMessage(args)); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
	if err := schema.ValidateJSON(json.RawMessage(`{"text":"hello"}`)); err != nil {
		t.Fatal(err)
	}
}
func TestPackageParsingRejectsUnknownFields(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"format":1,"id":"sample","version":"1","title":"Sample","files":[],"install_command":"curl bad"}`)); err == nil {
		t.Fatal("install hook field accepted")
	}
}
