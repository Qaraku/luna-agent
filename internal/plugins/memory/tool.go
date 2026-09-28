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
	// pair is deliberate: the capability has one append and one list, and
	// neither of them edits or removes anything.
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
	rememberDescription = "Store one durable fact about the user so a later session can use it. You can only append: this tool never edits or removes a stored fact, and luna_recall is how you see what is already stored. The user can see the stored facts and retract one in the dedicated Memory (记忆) panel, opened from the page header, so tell them where to remove it rather than refusing to store it."

	// recallDescription is the tool's model-visible description. It states when
	// to reach for it and what it will not do, because a listing tool the model
	// could mistake for a write is worse than no listing tool at all.
	recallDescription = "List the facts about the user that are in effect right now, each with the time it was recorded. Use it when you want to confirm what you already know about the user, or when you are unsure whether something was stored before; it only reads and never changes anything. A fact the user retracted is not in the list. The list is capped, and a list that stopped at the cap says how many of how many it returned and where it stopped. No stored fact at all is an ordinary answer, not an error."

	// recallEmpty is the whole model-visible result of a recall over an empty
	// store. An empty store is not a failure: it is a store that holds nothing
	// yet, and the honest answer says exactly that.
	recallEmpty = "no stored facts: nothing has been stored yet"
)

// RememberTool is the plugin's luna_remember: the write half of the model's
// reach into the store, and a write-only one. There is deliberately no parameter
// that could read, list, edit or retract a fact — reading is RecallTool's job
// and nothing the model can call changes or removes a stored fact.
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
// action with one parameter. additionalProperties is closed, so a parameter that
// is not `text` is refused before any store is reached.
func rememberSchema() *jsonschema.Schema {
	type args struct {
		Text string `json:"text" jsonschema_description:"One durable fact about the user, stated in a single sentence"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
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
	var in struct {
		Text string `json:"text"`
	}
	if err := decodeOne(arguments, &in); err != nil {
		return "", err
	}
	if err := validateFactText(in.Text); err != nil {
		return "", err
	}
	if t.store == nil {
		return "", errors.New("memory is not configured on this host")
	}
	info, _ := plugin.Run(ctx)
	if _, err := t.store.Remember(info.SessionID, in.Text, time.Now()); err != nil {
		// A store that cannot be written is an infrastructure failure, not a
		// refusal of this call: the model can do nothing about a full disk, and
		// a run that silently lost a fact the model was told it stored would be
		// lying. The marker makes the Kernel end the round.
		return "", plugin.Unavailable(fmt.Errorf("store the fact: %w", err))
	}
	return rememberConfirmation, nil
}

// RecallTool is the plugin's luna_recall: the read half of the model's reach
// into the store, and a read-only one. It takes no parameter at all, so there
// is no input that could name a fact to change or remove, and it never writes.
//
// It emits nothing, for the same reason RememberTool does not: the event stream
// belongs to the Kernel.
type RecallTool struct{ store *Store }

// NewRecallTool wires the tool to the store it reads.
func NewRecallTool(store *Store) *RecallTool { return &RecallTool{store: store} }

func (t *RecallTool) Name() string { return RecallToolName }

func (t *RecallTool) Description() string { return recallDescription }

func (t *RecallTool) Schema() *jsonschema.Schema { return recallSchema() }

// recallSchema is the exact public schema of the read tool: an object with no
// parameters and no additional properties, so a call carrying anything is
// refused before the store is reached.
func recallSchema() *jsonschema.Schema {
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	return r.Reflect(struct{}{})
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
		fmt.Fprintf(&b, "- %s (recorded %s)\n", singleLine(fact.Text), fact.At.UTC().Format(time.RFC3339Nano))
	}
	return b.String()
}

// Invoke lists the facts in effect. It never writes: a read leaves the store
// byte for byte as it found it.
//
// A store that cannot be read is an infrastructure failure, marked the same way
// a failed write is: a listing that quietly reported an empty memory would tell
// the model it remembers nothing, which is not what happened.
func (t *RecallTool) Invoke(_ context.Context, arguments string) (string, error) {
	// The tool takes no parameters, so the model calling it with no arguments
	// at all — an empty string — means the same as an empty object.
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	if err := decodeOne(arguments, &struct{}{}); err != nil {
		return "", err
	}
	if t.store == nil {
		return "", errors.New("memory is not configured on this host")
	}
	facts, err := t.store.Facts()
	if err != nil {
		return "", plugin.Unavailable(fmt.Errorf("read the stored facts: %w", err))
	}
	return recallListing(facts), nil
}
