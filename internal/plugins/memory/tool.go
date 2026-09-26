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
	// RememberToolName is the model-visible name of the memory tool.
	RememberToolName = "luna_remember"

	// rememberConfirmation is the whole model-visible result of a memory write.
	// It carries no plugin identity: the tool's owner is named in the
	// descriptor, not in every answer, and the model needs only to know the
	// write landed. No fact text is echoed back beyond what the model supplied.
	rememberConfirmation = "fact stored"

	// rememberDescription is the tool's model-visible description. It has to
	// keep the two facts the model can act on: the write is append-only, and
	// the user — not the model — is who removes a stored fact.
	rememberDescription = "Store one durable fact about the user so a later session can use it. You can only append: a stored fact cannot be read back or removed by you. The user can see the stored facts and retract one in the dedicated Memory (记忆) panel, opened from the page header, so tell them where to remove it rather than refusing to store it."
)

// RememberTool is the plugin's luna_remember: the model's only reach into the
// store, and a write-only one. There is deliberately no parameter that could
// read, list, edit or retract a fact.
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
// trailing JSON values. It is the tool's own copy of the rule the host-native
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
