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

// SkillViewTool 是按需读取技能正文的入口；管理工具另提供受控的修订检查。 Its boundary is the skill's own directory and it reuses
// internal/fileread rather than checking paths itself, so a skill read cannot
// reach anywhere the file tools could not.
//
// It holds the whole discovery, not just the skills in service, because the two
// refusals it can give are different answers: a name that was never discovered,
// and a name the user turned off. A nil state is the empty selection — nothing
// turned off.
type SkillViewTool struct {
	found   []skills.Skill
	state   *selection
	limit   int
	source  func(context.Context) ([]skills.Skill, error)
	library *Library
}

// NewSkillViewTool wires the tool to the skills discovery produced and to the
// selection the manifest also reads, so a skill that is not in the manifest is
// also not readable through the tool. limit is the single-read cap; 0 means
// fileread.DefaultLimit, the same 256 KiB the file tools use.
func NewSkillViewTool(found []skills.Skill, state *selection, limit ...int) *SkillViewTool {
	tool := &SkillViewTool{found: found, state: state, limit: fileread.DefaultLimit}
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
func (t *SkillViewTool) Invoke(ctx context.Context, arguments string) (string, error) {
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
	if err := plugin.CheckAccess(ctx, plugin.AccessRead); err != nil {
		return "", err
	}
	// A skill the user turned off is refused by name, not reported as never
	// discovered: "there is no such skill" would be a false statement about
	// this installation, and the model would have no way to tell that the
	// procedure it wants is on disk but out of service.
	if !plugin.ResourceSelected(ctx, PluginID, name) {
		return "", fmt.Errorf("the skill %q is not selected for this run", name)
	}
	if t.state.off(name) {
		return "", fmt.Errorf("the skill %q is turned off in the user's settings; it is not read while it is off", name)
	}
	available := t.found
	if t.source != nil {
		var err error
		available, err = t.source(ctx)
		if err != nil {
			return "", plugin.Unavailable(err)
		}
	}
	var selected *skills.Skill
	for i := range available {
		if available[i].Name == name {
			selected = &available[i]
			break
		}
	}
	if selected == nil {
		return "", fmt.Errorf("no skill named %q is installed here", name)
	}
	dir := selected.Dir
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
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: t.Name(), Summary: "read an installed skill file", Target: resolved, ReadRoots: []string{dir}, Permissions: []plugin.AccessKind{plugin.AccessRead}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		return "", err
	}
	text, err := fileread.Read(resolved, t.limit)
	if err != nil {
		return "", err
	}
	if t.library != nil && selected.Revision != "" {
		if err := t.library.Verify(name, selected.Revision, requested, text); err != nil {
			return "", plugin.Unavailable(err)
		}
	}
	if readTheSkill {
		// The frontmatter is what discovery read; the model is here for the
		// procedure, so it is not repeated.
		return skills.Body(text), nil
	}
	return text, nil
}

// The tool is a capability contribution, so it is exactly this interface and
// nothing about where it runs.
var _ plugin.Tool = (*SkillViewTool)(nil)
