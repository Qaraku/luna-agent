package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const UpdateToolName = "luna_memory_update"

func memoryAccess(ctx context.Context, s *Store, tool string, kind plugin.AccessKind, all bool, target, preview, arguments string) error {
	r := plugin.AccessRequest{Tool: tool, Summary: "access private memory", Target: target, Preview: preview, ParametersDigest: plugin.AccessDigest(arguments), Permissions: []plugin.AccessKind{kind}, ScopeApproval: all || kind == plugin.AccessWrite}
	if kind == plugin.AccessRead {
		r.ReadRoots = []string{filepath.Dir(s.path)}
	} else {
		r.WriteRoots = []string{filepath.Dir(s.path)}
	}
	return plugin.RequireAccess(ctx, r)
}
func memoryToolError(err error) error {
	var path *os.PathError
	if errors.Is(err, ErrCorrupt) || errors.As(err, &path) {
		return plugin.Unavailable(err)
	}
	return err
}

type UpdateTool struct{ store *Store }

func NewUpdateTool(s *Store) *UpdateTool { return &UpdateTool{s} }
func (*UpdateTool) Name() string         { return UpdateToolName }
func (*UpdateTool) Description() string {
	return "Correct a visible memory note or restore a stored earlier revision with user approval. Obtain a ref using luna_recall with include_refs=true. Corrections preserve scope and append before/after history; they do not erase the original. restore takes the source ref and expected_ref of the current revision in the same lineage, or an empty expected_ref only when that lineage has no active note. Only global and the host's current project are accessible. No deletion, project-id override or permission changes. A stale reference fails without replacing newer content."
}

type updateArgs struct {
	Action      string `json:"action" jsonschema:"enum=correct,enum=restore"`
	Ref         string `json:"ref"`
	Text        string `json:"text,omitempty"`
	ExpectedRef string `json:"expected_ref,omitempty"`
	Reason      string `json:"reason"`
}

func (*UpdateTool) Schema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(updateArgs{})
	s.Required = []string{"action", "ref", "reason"}
	return s
}
func (t *UpdateTool) Invoke(ctx context.Context, arguments string) (string, error) {
	var in updateArgs
	if len(arguments) > 8192 {
		return "", fmt.Errorf("memory update exceeds argument limit")
	}
	if err := decodeOne(arguments, &in); err != nil {
		return "", err
	}
	if in.Action != "correct" && in.Action != "restore" {
		return "", fmt.Errorf("action must be correct or restore")
	}
	if in.Ref == "" || len(in.Ref) > 128 || len(in.ExpectedRef) > 128 {
		return "", fmt.Errorf("a bounded memory reference is required")
	}
	if in.Action == "restore" && in.Text != "" || in.Action == "correct" && in.ExpectedRef != "" {
		return "", fmt.Errorf("correct takes text; restore takes expected_ref, not replacement text")
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 2048 {
		return "", fmt.Errorf("a bounded reason is required")
	}
	if err := plugin.CheckAccess(ctx, plugin.AccessRead, plugin.AccessWrite); err != nil {
		return "", err
	}
	if t.store == nil {
		return "", plugin.Unavailable(ErrNoFile)
	}
	if err := memoryAccess(ctx, t.store, UpdateToolName, plugin.AccessRead, false, in.Ref, "", arguments); err != nil {
		return "", err
	}
	fact, err := t.store.LookupRef(in.Ref)
	if err != nil {
		return "", memoryToolError(err)
	}
	info, _ := plugin.Run(ctx)
	visible, err := filterFacts([]Fact{fact}, "current", info.WorkspaceID)
	if err != nil {
		return "", err
	}
	if len(visible) == 0 {
		return "", fmt.Errorf("the note is outside the current memory scope")
	}
	snapshot, err := t.store.Snapshot()
	if err != nil {
		return "", memoryToolError(err)
	}
	if in.Action == "correct" && indexOfRef(snapshot.Facts, in.Ref) < 0 {
		return "", ErrMemoryConflict
	}
	if in.Action == "restore" {
		var current *Fact
		for _, candidate := range snapshot.Facts {
			if candidate.family() == fact.family() {
				if current != nil {
					return "", ErrMemoryConflict
				}
				value := candidate
				current = &value
			}
		}
		if (in.ExpectedRef == "" && current != nil) || (in.ExpectedRef != "" && (current == nil || current.Ref() != in.ExpectedRef || current.Ref() == in.Ref)) {
			return "", ErrMemoryConflict
		}
	}
	text := in.Text
	if in.Action == "restore" {
		text = fact.Text
	}
	if err := ValidateText(text); err != nil {
		return "", err
	}
	preview := fmt.Sprintf("%s\nScope: %s %s\nBefore: %s\nAfter: %s", in.Reason, normalizedScope(fact.Scope), fact.WorkspaceID, fact.Text, text)
	if in.Action == "restore" && in.ExpectedRef == "" {
		preview = in.Reason + "\nRestore withdrawn content: " + fact.Text
	}
	if in.Action == "restore" && in.ExpectedRef != "" {
		current, err := t.store.LookupRef(in.ExpectedRef)
		if err != nil {
			return "", memoryToolError(err)
		}
		if current.family() != fact.family() {
			return "", ErrMemoryConflict
		}
		preview = fmt.Sprintf("%s\nBefore: %s\nRestore: %s", in.Reason, current.Text, text)
	}
	if err := memoryAccess(ctx, t.store, UpdateToolName, plugin.AccessWrite, false, in.Ref, preview, arguments); err != nil {
		return "", err
	}
	origin := ChangeOrigin{Kind: "model", SessionID: info.SessionID, RunID: info.RunID, Reason: in.Reason}
	var change MemoryChange
	if in.Action == "correct" {
		change, err = t.store.Correct(ctx, in.Ref, text, origin)
	} else {
		change, err = t.store.Restore(ctx, in.Ref, in.ExpectedRef, origin)
	}
	if err != nil {
		return "", memoryToolError(err)
	}
	data, err := json.Marshal(map[string]any{"change": change, "ref": change.After.Ref()})
	return string(data), err
}
func (s *Store) LookupRef(ref string) (Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, _, err := readRecords(s.path)
	if err != nil {
		return Fact{}, err
	}
	fact, ok := findStoredRef(records, ref)
	if !ok {
		return Fact{}, ErrUnknownFact
	}
	return fact, nil
}
