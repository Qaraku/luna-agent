package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// memoryFact is one fact as the browser sees it. The stored record type is not
// part of this contract: everything in facts is a fact.
type memoryFact struct {
	Text          string    `json:"text"`
	At            time.Time `json:"at"`
	SourceSession string    `json:"source_session"`
}

// memoryRetractedFact is a fact that is no longer in effect, with when it was
// retracted.
type memoryRetractedFact struct {
	Text        string    `json:"text"`
	At          time.Time `json:"at"`
	RetractedAt time.Time `json:"retracted_at"`
}

type memoryView struct {
	Facts     []memoryFact          `json:"facts"`
	Retracted []memoryRetractedFact `json:"retracted"`
}

// factsRoute answers GET /api/memory with the user's view of their own store.
// The model has no equivalent: memory reaches it only through the labelled
// injection.
type factsRoute struct{ store *Store }

func (r factsRoute) Method() string { return http.MethodGet }
func (r factsRoute) Path() string   { return MemoryRoutePath }

func (r factsRoute) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if r.store == nil {
		fail(w, http.StatusInternalServerError, errors.New("memory is not configured"))
		return
	}
	snapshot, err := r.store.Snapshot()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	// Both lists are built empty rather than nil, so an empty memory is `[]` on
	// the wire and never `null`.
	view := memoryView{
		Facts:     make([]memoryFact, 0, len(snapshot.Facts)),
		Retracted: make([]memoryRetractedFact, 0, len(snapshot.Retracted)),
	}
	for _, fact := range snapshot.Facts {
		view.Facts = append(view.Facts, memoryFact{Text: fact.Text, At: fact.At, SourceSession: fact.SourceSession})
	}
	for _, gone := range snapshot.Retracted {
		view.Retracted = append(view.Retracted, memoryRetractedFact{Text: gone.Fact.Text, At: gone.Fact.At, RetractedAt: gone.RetractedAt})
	}
	send(w, http.StatusOK, view)
}

// retractRoute answers POST /api/memory/retract. The fact is named by its text
// and its timestamp together, so a retraction can only remove the fact it was
// made about, and a fact that is not in effect is a 404 rather than a silent
// success.
type retractRoute struct{ store *Store }

func (r retractRoute) Method() string { return http.MethodPost }
func (r retractRoute) Path() string   { return RetractRoutePath }

func (r retractRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		fail(w, http.StatusInternalServerError, errors.New("memory is not configured"))
		return
	}
	var in struct {
		At   string `json:"at"`
		Text string `json:"text"`
	}
	if !decode(w, req, &in) {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, in.At)
	if err != nil {
		fail(w, http.StatusBadRequest, errors.New("at must be an RFC3339 timestamp"))
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		fail(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	gone, err := r.store.Retract(at, in.Text)
	if err != nil {
		fail(w, memoryStatus(err), err)
		return
	}
	send(w, http.StatusOK, map[string]any{"retracted": memoryRetractedFact{
		Text:        gone.Fact.Text,
		At:          gone.Fact.At,
		RetractedAt: gone.RetractedAt,
	}})
}

// memoryStatus maps a store failure onto the HTTP status: retracting a fact that
// is not in effect is the caller's mistake, a memory file that cannot be read is
// the server's.
func memoryStatus(err error) int {
	switch {
	case errors.Is(err, ErrUnknownFact):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// send, fail and decode are this package's copies of the host's JSON helpers:
// the plugin owns its own wire behavior, so the routes do not reach into the
// HTTP layer for them.

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes the frozen error body. The error text never carries a host path:
// the store's own errors name lines, not files.
func fail(w http.ResponseWriter, status int, err error) {
	send(w, status, map[string]string{"error": err.Error()})
}

// decode accepts exactly one JSON object of at most 32768 bytes, rejecting
// unknown fields and trailing JSON. The body cap is this route's own guard; the
// Kernel keeps its own.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds 32768 bytes"))
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, err)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, http.StatusBadRequest, fmt.Errorf("expected exactly one JSON object"))
		return false
	}
	return true
}
