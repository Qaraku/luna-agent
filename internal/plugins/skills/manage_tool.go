package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Qaraku/luna-agent/internal/plugin"
	jsonschema "github.com/eino-contrib/jsonschema"
)

type manageRequest struct {
	Action           string           `json:"action" jsonschema:"enum=list,enum=read,enum=preview,enum=save,enum=restore" jsonschema_description:"list/read inspect learned skills; preview returns a draft diff without writing; save/restore require user approval before publishing a revision"`
	Name             string           `json:"name,omitempty" jsonschema_description:"Learned skill name for read/restore"`
	Revision         string           `json:"revision,omitempty" jsonschema_description:"Immutable revision to read or restore; omit for latest when reading"`
	Definition       *SkillDefinition `json:"definition,omitempty" jsonschema_description:"For preview/save: name, description, reusable Markdown body, and optional references/*.md text files. Not conversation logs or credentials"`
	ExpectedRevision string           `json:"expected_revision,omitempty" jsonschema_description:"Current revision required to update or restore; empty only for creation"`
	Reason           string           `json:"reason,omitempty" jsonschema_description:"Why this reusable method should be saved; refer to the user's teaching or correction, not unverified success"`
	Offset           int              `json:"offset,omitempty"`
	Limit            int              `json:"limit,omitempty"`
}
type manageTool struct{ p *Plugin }

func (manageTool) Name() string { return ManageToolName }
func (manageTool) Description() string {
	return "Maintain reusable workflows in the personal skill library when the user asks to preserve a method or teaches a reusable correction. Preview before saving; capture concise steps and pitfalls, not full chat logs, credentials or unverified claims. Saves and restores require the current revision, preserve history and request approval before writes. Installed source skills are not overwritten: adapt them under a new learned name. New or updated skills take effect in subsequent runs; this run keeps its selected revisions. This tool does not install executable plugins, change permissions, or train a model."
}
func (manageTool) Schema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(manageRequest{})
	s.Required = []string{"action"}
	return s
}
func managedError(err error) error {
	var path *os.PathError
	if errors.Is(err, ErrLibraryCorrupt) || errors.As(err, &path) {
		return plugin.Unavailable(err)
	}
	return err
}
func (t manageTool) Invoke(ctx context.Context, arguments string) (string, error) {
	result, err := t.invoke(ctx, arguments)
	return result, managedError(err)
}
func (t manageTool) invoke(ctx context.Context, arguments string) (string, error) {
	if !utf8.ValidString(arguments) || len(arguments) > MaxSkillBundleBytes*6+16*1024 {
		return "", fmt.Errorf("skill request exceeds the text limit")
	}
	var in *manageRequest
	if err := decodeOne(arguments, &in); err != nil {
		return "", err
	}
	if in == nil {
		return "", fmt.Errorf("request must be an object")
	}
	write := in.Action == "save" || in.Action == "restore"
	switch in.Action {
	case "list", "read", "preview", "save", "restore":
	default:
		return "", fmt.Errorf("unknown skill action")
	}
	kinds := []plugin.AccessKind{plugin.AccessRead}
	if write {
		kinds = append(kinds, plugin.AccessWrite)
	}
	if err := plugin.CheckAccess(ctx, kinds...); err != nil {
		return "", err
	}
	if write {
		run, ok := plugin.Run(ctx)
		if !ok || run.SessionID == "" || run.RunID == "" {
			return "", fmt.Errorf("saving a skill requires current run and session attribution")
		}
	}
	if in.Offset < 0 || in.Limit < 0 || in.Limit > 50 {
		return "", fmt.Errorf("offset must be nonnegative and limit must be 1..50")
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: ManageToolName, Summary: "read personal skill library metadata", Target: in.Name, ReadRoots: []string{t.p.library.dir}, Permissions: []plugin.AccessKind{plugin.AccessRead}, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		return "", err
	}
	encode := func(v any) (string, error) { data, err := json.Marshal(v); return string(data), err }
	if in.Action == "list" {
		entries, err := t.p.library.List()
		if err != nil {
			return "", err
		}
		selected := []SkillRevision{}
		for _, entry := range entries {
			if plugin.ResourceSelected(ctx, PluginID, entry.Name) && !t.p.state.off(entry.Name) {
				selected = append(selected, entry)
			}
		}
		total := len(selected)
		start := min(in.Offset, total)
		end := min(start+in.Limit, total)
		return encode(map[string]any{"entries": selected[start:end], "total": total, "offset": start, "remaining": total - end})
	}
	name := in.Name
	if in.Definition != nil {
		if name != "" && name != in.Definition.Name {
			return "", fmt.Errorf("name and definition disagree")
		}
		name = in.Definition.Name
	}
	if !learnedName.MatchString(name) {
		return "", fmt.Errorf("valid skill name is required")
	}
	if err := t.p.checkLearnedName(name); err != nil {
		return "", err
	}
	if (in.Action == "read" || in.Action == "restore" || in.ExpectedRevision != "") && (!plugin.ResourceSelected(ctx, PluginID, name) || t.p.state.off(name)) {
		return "", fmt.Errorf("skill is not selected or is disabled")
	}
	if in.Action == "read" {
		entry, err := t.p.library.Lookup(name, in.Revision)
		if err != nil {
			return "", err
		}
		definition, err := t.p.library.Definition(name, entry.Revision)
		if err != nil {
			return "", err
		}
		return encode(map[string]any{"entry": entry, "definition": definition})
	}
	var definition SkillDefinition
	if in.Action == "restore" {
		if in.Revision == "" || in.ExpectedRevision == "" {
			return "", fmt.Errorf("restore requires revision and expected_revision")
		}
		var err error
		definition, err = t.p.library.Definition(name, in.Revision)
		if err != nil {
			return "", err
		}
	} else {
		if in.Definition == nil {
			return "", fmt.Errorf("definition is required")
		}
		definition = *in.Definition
	}
	preview, err := t.p.library.Preview(definition, in.ExpectedRevision)
	if err != nil {
		return "", err
	}
	if in.Action == "preview" {
		return encode(preview)
	}
	if strings.TrimSpace(in.Reason) == "" || !textOK(in.Reason, 1024) {
		return "", fmt.Errorf("a bounded reason is required before saving a skill")
	}
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: ManageToolName, Summary: "publish a personal skill revision", Target: name, Preview: in.Reason + "\n\n" + preview.Diff, WriteRoots: []string{t.p.library.dir}, Permissions: []plugin.AccessKind{plugin.AccessWrite}, ParametersDigest: plugin.AccessDigest(arguments), ScopeApproval: true}); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	run, _ := plugin.Run(ctx)
	saved, err := t.p.library.SaveContext(ctx, definition, in.ExpectedRevision, SkillOrigin{Kind: "model", SessionID: run.SessionID, RunID: run.RunID, Reason: in.Reason})
	if err != nil {
		return "", err
	}
	return encode(map[string]any{"saved": saved, "effective": "subsequent runs when enabled and selected; current resource snapshot is unchanged"})
}
