package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/skills"
	jsonschema "github.com/eino-contrib/jsonschema"
)

// skillViewDescription is the tool's model-visible description. It has to say
// when to reach for it: the manifest in the context names skills that may not
// apply, so the trigger is "one of them looks relevant to the task in hand".
const skillViewDescription = "Read one installed skill: its own " + skills.FileName + " when no path is given, or one file inside that skill's directory. Use it when the skills list in your context has an entry that looks relevant to the task in hand — read that skill before starting that kind of work, and follow it where it applies. " +
	"`name` is the skill's name as it appears in that list. `path` is optional and names a file inside the skill's own directory, relative to it; a path that leaves that directory is refused. Frontmatter fields at the top of a " + skills.FileName + " are not part of the answer."

// SkillViewTool is luna_skill_view: the read side of a skill, and the only way
// one is opened. Its boundary is the skill's own directory and it reuses
// internal/fileread rather than checking paths itself, so a skill read cannot
// reach anywhere the file tools could not.
type SkillViewTool struct {
	found []skills.Skill
	limit int
}

// NewSkillViewTool wires the tool to the skills discovery produced. limit is the
// single-read cap; 0 means fileread.DefaultLimit, the same 256 KiB the file
// tools use.
func NewSkillViewTool(found []skills.Skill, limit ...int) *SkillViewTool {
	tool := &SkillViewTool{found: found, limit: fileread.DefaultLimit}
	if len(limit) > 0 && limit[0] > 0 {
		tool.limit = limit[0]
	}
	return tool
}

func (t *SkillViewTool) Name() string { return SkillToolName }

func (t *SkillViewTool) Description() string { return skillViewDescription }

func (t *SkillViewTool) Schema() *jsonschema.Schema { return skillViewSchema() }

// skillViewSchema is the tool's exact public schema: one skill name, and an
// optional path inside it. additionalProperties is closed, so a parameter that
// is neither is refused before any file is reached.
func skillViewSchema() *jsonschema.Schema {
	type args struct {
		Name string `json:"name" jsonschema_description:"The skill's name, as it appears in the skills list in your context"`
		Path string `json:"path,omitempty" jsonschema_description:"Optional file inside the skill's directory, relative to it; omit to read the skill itself"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"name"}
	return s
}

// decodeOne accepts exactly one JSON object and rejects unknown fields and
// trailing JSON values, the same rule the kernel's wrappers and the memory tool
// follow: a malformed call is refused before it reaches the filesystem.
func decodeOne(arguments string, into any) error {
	d := json.NewDecoder(strings.NewReader(arguments))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	var extra any
	if extraErr := d.Decode(&extra); extraErr != io.EOF {
		if extraErr == nil {
			return errors.New("expected exactly one JSON object")
		}
		return extraErr
	}
	return nil
}

// Invoke reads one skill. Every refusal here is a refusal of this call and not a
// failure of the round: the model supplied the name and the path and can correct
// both. Nothing is ever truncated — a skill whose file is over the limit, or one
// that is not text, is refused with the reason instead.
func (t *SkillViewTool) Invoke(_ context.Context, arguments string) (string, error) {
	var in struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := decodeOne(arguments, &in); err != nil {
		return "", err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return "", errors.New("name is required")
	}
	dir, ok := t.dirOf(name)
	if !ok {
		return "", fmt.Errorf("no skill named %q is installed here", name)
	}
	// The skill's own directory is the root of this read, so no path the model
	// sends can leave the one skill it named. An empty path means the skill
	// file itself.
	requested := strings.TrimSpace(in.Path)
	readTheSkill := requested == ""
	if readTheSkill {
		requested = skills.FileName
	}
	resolved, err := fileread.Resolve(dir, requested, t.limit)
	if err != nil {
		return "", err
	}
	// Reading is fileread's job too: the size cap after the bytes were actually
	// read, the NUL check and the regular-file check live in one place, and a
	// growth between validation and read is caught there rather than here.
	text, err := fileread.Read(resolved, t.limit)
	if err != nil {
		return "", err
	}
	if readTheSkill {
		// The frontmatter is what discovery read; the model is here for the
		// procedure, so it is not repeated.
		return skills.Body(text), nil
	}
	return text, nil
}

// dirOf finds one discovered skill by name. The manifest the model read is built
// from exactly this list, so a name that is not here is one the model did not
// see.
func (t *SkillViewTool) dirOf(name string) (string, bool) {
	for _, skill := range t.found {
		if skill.Name == name {
			return skill.Dir, true
		}
	}
	return "", false
}

// The tool is a capability contribution, so it is exactly this interface and
// nothing about where it runs.
var _ plugin.Tool = (*SkillViewTool)(nil)
