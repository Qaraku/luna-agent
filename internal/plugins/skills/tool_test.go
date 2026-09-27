package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
	lunas "github.com/Qaraku/luna-agent/internal/skills"
)

const sampleSkill = "---\nname: alpha\ndescription: Does alpha.\n---\n\n# Alpha\n\nStep one.\n"

// newSkillDir builds <tmp>/<name>/SKILL.md and returns the skill discovery would
// have produced for it.
func newSkillDir(t *testing.T, name, content string) (lunas.Skill, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lunas.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return lunas.Skill{Name: name, Description: "Does alpha.", Dir: dir, Scope: lunas.ScopeUser}, dir
}

func invoke(t *testing.T, tool *SkillViewTool, arguments string) (string, error) {
	t.Helper()
	return tool.Invoke(context.Background(), arguments)
}

func TestReadsTheSkillWithoutItsFrontmatter(t *testing.T) {
	skill, _ := newSkillDir(t, "alpha", sampleSkill)
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	body, err := invoke(t, tool, `{"name":"alpha"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "# Alpha") || !strings.Contains(body, "Step one.") {
		t.Fatalf("body=%q, want the skill's own text", body)
	}
	if strings.Contains(body, "description: Does alpha.") || strings.Contains(body, "name: alpha") {
		t.Fatalf("body=%q, want the frontmatter left out", body)
	}
}

func TestReadsAFileInsideTheSkillDirectory(t *testing.T) {
	skill, dir := newSkillDir(t, "alpha", sampleSkill)
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("Details.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	body, err := invoke(t, tool, `{"name":"alpha","path":"notes.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Details.\n" {
		t.Fatalf("body=%q, want the named file", body)
	}
}

func TestRefusesAPathOutsideTheSkillDirectory(t *testing.T) {
	skill, dir := newSkillDir(t, "alpha", sampleSkill)
	// A sibling skill next to this one, plus a file one level up: neither is
	// reachable through this tool.
	neighbour := filepath.Join(filepath.Dir(dir), "beta")
	if err := os.MkdirAll(neighbour, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(neighbour, lunas.FileName), []byte("---\nname: beta\ndescription: B.\n---\n\n# Beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	for _, path := range []string{"../beta/" + lunas.FileName, "../secret.txt", "/etc/passwd", "../../etc/passwd"} {
		arguments, err := json.Marshal(map[string]string{"name": "alpha", "path": path})
		if err != nil {
			t.Fatal(err)
		}
		body, err := invoke(t, tool, string(arguments))
		if err == nil {
			t.Fatalf("path %q was read as %q", path, body)
		}
		if plugin.IsUnavailable(err) {
			t.Fatalf("path %q ended the round: %v", path, err)
		}
		if !strings.Contains(err.Error(), "read root") {
			t.Fatalf("path %q err=%v, want the boundary named", path, err)
		}
	}
}

func TestRefusesAnUnknownSkill(t *testing.T) {
	skill, _ := newSkillDir(t, "alpha", sampleSkill)
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	_, err := invoke(t, tool, `{"name":"beta"}`)
	if err == nil || !strings.Contains(err.Error(), `no skill named "beta"`) {
		t.Fatalf("err=%v, want a refusal naming the skill", err)
	}
	if plugin.IsUnavailable(err) {
		t.Fatal("an unknown name is the call's own error, not a failure of the round")
	}
	// The list is empty, so every name is unknown — and the refusal says so
	// rather than reading something.
	if _, err := invoke(t, NewSkillViewTool(nil, nil), `{"name":"alpha"}`); err == nil {
		t.Fatal("a name was accepted with no skills discovered")
	}
}

func TestRefusesAFileOverTheLimit(t *testing.T) {
	skill, dir := newSkillDir(t, "alpha", sampleSkill)
	long := strings.Repeat("x", 4096)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil, 1024)

	// The skill file itself is inside the limit: only the over-limit file is
	// refused, so a limiter that refused everything would not pass below.
	if body, err := invoke(t, tool, `{"name":"alpha"}`); err != nil {
		t.Fatalf("the skill file is inside the limit: %v", err)
	} else if !strings.Contains(body, "Step one.") {
		t.Fatalf("body=%q", body)
	}

	body, err := invoke(t, tool, `{"name":"alpha","path":"long.txt"}`)
	if err == nil {
		t.Fatalf("an over-limit file was read: %q", body)
	}
	if !strings.Contains(err.Error(), "single-read limit") {
		t.Fatalf("err=%v, want the limit named", err)
	}
	if strings.Contains(body, long) {
		t.Fatal("the file was returned despite being over the limit")
	}
}

func TestRefusesAFileThatIsNotText(t *testing.T) {
	skill, dir := newSkillDir(t, "alpha", sampleSkill)
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("head\x00tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	body, err := invoke(t, tool, `{"name":"alpha","path":"blob.bin"}`)
	if err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("body=%q err=%v, want a refusal naming the NUL byte", body, err)
	}
}

func TestRefusesAMissingFile(t *testing.T) {
	skill, _ := newSkillDir(t, "alpha", sampleSkill)
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	_, err := invoke(t, tool, `{"name":"alpha","path":"absent.md"}`)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err=%v, want a not-found refusal", err)
	}
}

func TestRefusesMalformedArguments(t *testing.T) {
	skill, _ := newSkillDir(t, "alpha", sampleSkill)
	tool := NewSkillViewTool([]lunas.Skill{skill}, nil)

	cases := map[string]string{
		"no name":     `{"path":"notes.md"}`,
		"blank name":  `{"name":"  "}`,
		"extra field": `{"name":"alpha","other":1}`,
		"not json":    `name=alpha`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := invoke(t, tool, arguments); err == nil {
				t.Fatalf("arguments %s were accepted", arguments)
			}
		})
	}
}

func TestTheToolStatesWhenToUseItAndRequiresAName(t *testing.T) {
	tool := NewSkillViewTool(nil, nil)
	if tool.Name() != SkillToolName {
		t.Fatalf("name=%q", tool.Name())
	}
	description := tool.Description()
	if !strings.Contains(description, "skills list in your context") || !strings.Contains(description, "before starting") {
		t.Fatalf("description=%q, want it to say when the tool applies", description)
	}
	schema := tool.Schema()
	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Fatalf("required=%v, want exactly name", schema.Required)
	}
}
