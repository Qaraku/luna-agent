package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/plugin"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const (
	// RememberToolName is the model-visible name of the memory write tool.
	RememberToolName = "luna_remember"

	// RecallToolName is the model-visible name of the memory read tool. The
	// 读取与追加分开；纠错由独立的审批工具提供。
	RecallToolName = "luna_recall"

	// rememberConfirmation is the whole model-visible result of a memory write.
	// It carries no plugin identity: the tool's owner is named in the
	// descriptor, not in every answer, and the model needs only to know the
	// write landed. No fact text is echoed back beyond what the model supplied.
	rememberConfirmation = "fact stored"

	// rememberDescription is the tool's model-visible description. It has to
	// keep the two facts the model can act on: the write is append-only, and
	// the user — not the model — is who removes a stored fact. It points at
	// luna_recall for reading rather than telling the model the write is blind.
	rememberDescription = "Store one durable note. scope=global (default, for compatibility) is for personal preferences; scope=project is for knowledge about the current host-bound workspace. The model cannot choose another workspace id. Saving requires the current write policy and approval for the private memory scope. Use luna_recall first when unsure whether a note already exists. This tool only adds; use luna_memory_update for approved corrections and let the user retract entries in the Memory (记忆) panel opened from the page header."

	// recallDescription is the tool's model-visible description. It states when
	// to reach for it and what it will not do, because a listing tool the model
	// could mistake for a write is worse than no listing tool at all.
	recallDescription = "List the facts about the user that are in effect right now, each with the time it was recorded. Use it when you want to confirm what you already know about the user, or when you are unsure whether something was stored before; it only reads and never changes anything. A fact the user retracted is not in the list. Default visibility is global notes plus the current project; scope may be current, global, project or all. all requires extra read-scope approval. Set include_refs=true to obtain immutable references for corrections. The list is capped, and a list that stopped at the cap says how many of how many it returned and where it stopped. No stored fact at all is an ordinary answer, not an error. With no arguments, return the newest 50 facts at most, displayed oldest first. To find older facts, query is an optional case-insensitive literal substring of fact text, not a regex; it searches all effective facts before paging. offset skips that many newer matching facts (default 0), and limit sets the page size (1..50, default 50). Each page is still displayed oldest first. Use next_offset with the same query to read the next older page; when a default listing stops at 50, start the next page at offset 50. Each call reads the current facts, so new or retracted facts may shift offsets; this is not a frozen snapshot."

	// recallEmpty is the whole model-visible result of a recall over an empty
	// store. An empty store is not a failure: it is a store that holds nothing
	// yet, and the honest answer says exactly that.
	recallEmpty = "no stored facts: nothing has been stored yet"
)

// RememberTool is the plugin's luna_remember: the write half of the model's
// reach into the store, and a write-only one. 这个工具只追加；读取与审批式纠错
// 分别由 RecallTool 和 UpdateTool 负责，撤回仍是用户操作。
//
// It emits nothing. tool.started, tool.failed and tool.finished are the Kernel's
// to emit for this round — the plugin's job is the arguments and the store.
type RememberTool struct{ store *Store }

// NewRememberTool wires the tool to the store it appends to.
func NewRememberTool(store *Store) *RememberTool { return &RememberTool{store: store} }

func (t *RememberTool) Name() string { return RememberToolName }

func (t *RememberTool) Description() string { return rememberDescription }

func (t *RememberTool) Schema() *jsonschema.Schema { return rememberSchema() }

// rememberSchema is the exact public schema of the memory tool: one write-only
// action with text and scope. additionalProperties is closed; models cannot
// provide a workspace id, stored origin, or authority.
type rememberArgs struct {
	Text  string `json:"text" jsonschema_description:"One durable note, at most 500 characters"`
	Scope string `json:"scope,omitempty" jsonschema:"enum=global,enum=project" jsonschema_description:"global personal preferences (default), or the current host-bound project; never another project id"`
}

func rememberSchema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(rememberArgs{})
	s.Required = []string{"text"}
	return s
}

// validateFactText states the two input rules in the tool's own words, so the
// model gets a plain error instead of a store-internal one. The store enforces
// the same cap again rather than trusting a caller.
//
// These are call-level refusals: the model supplied the text and can correct it,
// so none of them carries plugin.ErrUnavailable.
func validateFactText(text string) error {
	switch err := ValidateText(text); {
	case err == nil:
		return nil
	case errors.Is(err, ErrEmptyFact):
		return errors.New("text is required")
	case errors.Is(err, ErrFactTooLong):
		return fmt.Errorf("text is longer than %d characters", MaxFactChars)
	default:
		return err
	}
}

// decodeOne accepts exactly one JSON object and rejects unknown fields and
// trailing JSON values. It is the tool's own copy of the rule the kernel's
// wrappers share: a malformed call must be refused before it reaches the store.
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

// Invoke appends one durable fact.
//
// The run identity comes from ctx. When the host set none, the fact is stored
// with an empty source session: no attribution is the honest answer, and a
// fabricated id would be worse than none.
func (t *RememberTool) Invoke(ctx context.Context, arguments string) (string, error) {
	var in rememberArgs
	if err := decodeOne(arguments, &in); err != nil {
		return "", err
	}
	if err := validateFactText(in.Text); err != nil {
		return "", err
	}
	if err := plugin.CheckAccess(ctx, plugin.AccessWrite); err != nil {
		return "", err
	}
	if t.store == nil {
		return "", errors.New("memory is not configured on this host")
	}
	info, _ := plugin.Run(ctx)
	scope := normalizedScope(in.Scope)
	workspace := ""
	if scope == ScopeProject {
		workspace = info.WorkspaceID
	}
	if !validFactScope(Fact{Scope: scope, WorkspaceID: workspace}) {
		return "", fmt.Errorf("scope must be global or a bound project")
	}
	if err := memoryAccess(ctx, t.store, RememberToolName, plugin.AccessWrite, false, scope+":"+workspace, in.Text, arguments); err != nil {
		return "", err
	}
	if _, err := t.store.RememberScoped(ctx, info.SessionID, info.RunID, in.Text, scope, workspace, "model"); err != nil {
		return "", memoryToolError(fmt.Errorf("store the fact: %w", err))
	}
	return rememberConfirmation, nil
}

// RecallTool 是 Memory 的只读入口：可按事实文本检索并分页读取当前生效集合。
// 参数只能选择读取范围，不会编辑、撤回或写入任何事实。
//
// It emits nothing, for the same reason RememberTool does not: the event stream
// belongs to the Kernel.
type RecallTool struct{ store *Store }

// NewRecallTool wires the tool to the store it reads.
func NewRecallTool(store *Store) *RecallTool { return &RecallTool{store: store} }

func (t *RecallTool) Name() string { return RecallToolName }

func (t *RecallTool) Description() string { return recallDescription }

func (t *RecallTool) Schema() *jsonschema.Schema { return recallSchema() }

// recallSchema 声明可选的查询和分页参数；未声明字段在读取存储前拒绝。
// 所有参数均可省略，原有空参数调用仍是同一个最新事实窗口。
func recallSchema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	return r.Reflect(recallOptions{})
}

// plural renders "1 fact" / "2 facts". It exists so the model-visible lines
// never read "1 facts".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// recallListing renders the model-visible answer for a read of the store: the
// facts in effect, oldest first, one line each, carrying the fact's own text and
// the time it was recorded.
//
// A listing longer than MaxRecallFacts keeps the newest facts — the oldest go
// first, the same direction the injection drops them — and says exactly what
// happened: how many were returned, how many are in effect, and which end was
// cut. A silent cut would let the model read a window as if it were the whole
// store.
func recallListing(facts []Fact) string {
	if len(facts) == 0 {
		return recallEmpty
	}
	kept := facts
	if len(facts) > MaxRecallFacts {
		kept = facts[len(facts)-MaxRecallFacts:]
	}
	var b strings.Builder
	if len(kept) < len(facts) {
		fmt.Fprintf(&b, "%d of %d stored facts in effect (the list stopped at %s; the %s older are not listed):\n",
			len(kept), len(facts), plural(MaxRecallFacts, "fact"), plural(len(facts)-len(kept), "fact"))
	} else {
		fmt.Fprintf(&b, "%s in effect:\n", plural(len(kept), "fact"))
	}
	for _, fact := range kept {
		// One line per fact, and the recorded time exactly as stored (UTC), so
		// the model can tell two similar facts apart by when they were written.
		fmt.Fprintf(&b, "- %s (recorded %s)\n", singleLine(scopedFactText(fact)), fact.At.UTC().Format(time.RFC3339Nano))
	}
	return b.String()
}

// Invoke lists the facts in effect. It never writes: a read leaves the store
// byte for byte as it found it.
//
// A store that cannot be read is an infrastructure failure, marked the same way
// a failed write is: a listing that quietly reported an empty memory would tell
// the model it remembers nothing, which is not what happened.
func (t *RecallTool) Invoke(ctx context.Context, arguments string) (string, error) {
	options, err := parseRecallOptions(arguments)
	if err != nil {
		return "", err
	}
	if t.store == nil {
		return "", errors.New("memory is not configured on this host")
	}
	if err := plugin.CheckAccess(ctx, plugin.AccessRead); err != nil {
		return "", err
	}
	if err := memoryAccess(ctx, t.store, RecallToolName, plugin.AccessRead, options.Scope == "all", options.Scope, "", arguments); err != nil {
		return "", err
	}
	facts, err := t.store.Facts()
	if err != nil {
		return "", plugin.Unavailable(fmt.Errorf("read the stored facts: %w", err))
	}
	info, _ := plugin.Run(ctx)
	facts, err = filterFacts(facts, options.Scope, info.WorkspaceID)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return recallPage(facts, options), nil
}
