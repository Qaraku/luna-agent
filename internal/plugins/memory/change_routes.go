package memory

import (
	"fmt"
	"net/http"
	"sort"
)

const CorrectRoutePath = "/api/memory/correct"
const RestoreRoutePath = "/api/memory/restore"
const AddRoutePath = "/api/memory/add"
const ExportRoutePath = "/api/memory/export"

type memoryChangeView struct {
	ID     string       `json:"id"`
	Action string       `json:"action"`
	Before memoryFact   `json:"before"`
	After  memoryFact   `json:"after"`
	Origin ChangeOrigin `json:"origin"`
}
type memoryScopeView struct {
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Count       int    `json:"count"`
}

func factView(f Fact) memoryFact {
	return memoryFact{Text: f.Text, At: f.At, SourceSession: f.SourceSession, SourceRun: f.SourceRun, Origin: f.Origin, Scope: normalizedScope(f.Scope), WorkspaceID: f.WorkspaceID, Ref: f.Ref(), Lineage: f.family()}
}
func changeView(c MemoryChange) memoryChangeView {
	return memoryChangeView{ID: c.ID, Action: c.Action, Before: factView(c.Before), After: factView(c.After), Origin: c.Origin}
}
func retractedView(g Retracted) memoryRetractedFact {
	return memoryRetractedFact{Text: g.Fact.Text, At: g.Fact.At, RetractedAt: g.RetractedAt, Ref: g.Fact.Ref(), Lineage: g.Fact.family(), Scope: normalizedScope(g.Fact.Scope), WorkspaceID: g.Fact.WorkspaceID, SourceSession: g.Fact.SourceSession, Origin: g.Fact.Origin}
}
func scopedMemoryView(snapshot Snapshot, scope, workspace string) (memoryView, error) {
	facts, err := filterFacts(snapshot.Facts, scope, workspace)
	if err != nil {
		return memoryView{}, err
	}
	view := memoryView{Facts: []memoryFact{}, Retracted: []memoryRetractedFact{}, Changes: []memoryChangeView{}, Scopes: []memoryScopeView{}}
	scopes := map[string]memoryScopeView{"": {Scope: ScopeGlobal}}
	for _, fact := range snapshot.Facts {
		key := fact.WorkspaceID
		entry := scopes[key]
		entry.Scope = normalizedScope(fact.Scope)
		entry.WorkspaceID = key
		entry.Count++
		scopes[key] = entry
	}
	for _, fact := range facts {
		view.Facts = append(view.Facts, factView(fact))
	}
	for _, gone := range snapshot.Retracted {
		if _, ok := scopes[gone.Fact.WorkspaceID]; !ok {
			scopes[gone.Fact.WorkspaceID] = memoryScopeView{Scope: normalizedScope(gone.Fact.Scope), WorkspaceID: gone.Fact.WorkspaceID}
		}
		visible, _ := filterFacts([]Fact{gone.Fact}, scope, workspace)
		if len(visible) > 0 {
			view.Retracted = append(view.Retracted, retractedView(gone))
		}
	}
	for _, change := range snapshot.Changes {
		visible, _ := filterFacts([]Fact{change.After}, scope, workspace)
		if len(visible) > 0 {
			view.Changes = append(view.Changes, changeView(change))
		}
	}
	keys := make([]string, 0, len(scopes))
	for key := range scopes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		view.Scopes = append(view.Scopes, scopes[key])
	}
	return view, nil
}

type memoryMutationRoute struct {
	store  *Store
	action string
}

func (r memoryMutationRoute) Method() string { return http.MethodPost }
func (r memoryMutationRoute) Path() string   { return "/api/memory/" + r.action }
func (r memoryMutationRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Ref         string `json:"ref"`
		ExpectedRef string `json:"expected_ref"`
		Text        string `json:"text"`
		Reason      string `json:"reason"`
		Scope       string `json:"scope"`
		WorkspaceID string `json:"workspace_id"`
	}
	if !decode(w, req, &in) {
		return
	}
	if r.store == nil {
		fail(w, 500, ErrNoFile)
		return
	}
	if r.action == "add" {
		if in.Ref != "" || in.ExpectedRef != "" {
			fail(w, 400, fmt.Errorf("new notes do not take references"))
			return
		}
		if err := ValidateText(in.Text); err != nil {
			fail(w, 400, err)
			return
		}
		if !validFactScope(Fact{Scope: normalizedScope(in.Scope), WorkspaceID: in.WorkspaceID}) {
			fail(w, 400, fmt.Errorf("invalid memory scope"))
			return
		}
		fact, err := r.store.RememberScoped(req.Context(), "", "", in.Text, in.Scope, in.WorkspaceID, "user")
		if err != nil {
			fail(w, memoryStatus(err), err)
			return
		}
		send(w, 200, map[string]any{"fact": factView(fact)})
		return
	}
	if in.Ref == "" {
		fail(w, 400, fmt.Errorf("ref is required"))
		return
	}
	if in.Scope != "" || in.WorkspaceID != "" {
		fail(w, 400, fmt.Errorf("corrections and restores preserve the original scope"))
		return
	}
	var change MemoryChange
	var err error
	if r.action == "correct" {
		if in.ExpectedRef != "" {
			fail(w, 400, fmt.Errorf("correct uses ref as its expected current reference"))
			return
		}
		if err := ValidateText(in.Text); err != nil {
			fail(w, 400, err)
			return
		}
		change, err = r.store.Correct(req.Context(), in.Ref, in.Text, ChangeOrigin{Kind: "user", Reason: in.Reason})
	} else {
		if in.Text != "" {
			fail(w, 400, fmt.Errorf("restore uses stored text, not replacement text"))
			return
		}
		change, err = r.store.Restore(req.Context(), in.Ref, in.ExpectedRef, ChangeOrigin{Kind: "user", Reason: in.Reason})
	}
	if err != nil {
		fail(w, memoryStatus(err), err)
		return
	}
	send(w, 200, map[string]any{"change": changeView(change), "fact": factView(change.After)})
}

type exportMemoryRoute struct{ store *Store }

func (exportMemoryRoute) Method() string { return http.MethodGet }
func (exportMemoryRoute) Path() string   { return ExportRoutePath }
func (r exportMemoryRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		fail(w, 500, ErrNoFile)
		return
	}
	snapshot, err := r.store.Snapshot()
	if err != nil {
		fail(w, 500, err)
		return
	}
	scope := req.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	view, err := scopedMemoryView(snapshot, scope, req.URL.Query().Get("workspace"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	send(w, 200, map[string]any{"facts": view.Facts, "retracted": view.Retracted, "changes": view.Changes})
}
