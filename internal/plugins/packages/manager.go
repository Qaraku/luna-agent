package packages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Qaraku/luna-agent/internal/atomicfile"
	"github.com/Qaraku/luna-agent/internal/plugin"
)

const MaxInstalledPackages = 32
const maxCatalogBytes = 8 * 1024 * 1024
const catalogName = "catalog.json"

type VersionInfo struct {
	Revision    string   `json:"revision"`
	Version     string   `json:"version"`
	Title       string   `json:"title"`
	Source      Source   `json:"source"`
	Access      []string `json:"access"`
	StateSchema int      `json:"state_schema"`
	Reviewed    bool     `json:"reviewed"`
	UITrusted   bool     `json:"ui_trusted"`
}
type Installed struct {
	RuntimeState string        `json:"runtime_state,omitempty"`
	ID           string        `json:"id"`
	Current      string        `json:"current"`
	Enabled      bool          `json:"enabled"`
	Removed      bool          `json:"removed"`
	Versions     []VersionInfo `json:"versions"`
	Problem      string        `json:"problem,omitempty"`
}
type Catalog struct {
	Format   int                  `json:"format"`
	Packages map[string]Installed `json:"packages"`
}
type Activation struct {
	ID            string `json:"id"`
	Revision      string `json:"revision"`
	Expected      string `json:"expected_current"`
	Confirm       bool   `json:"confirm"`
	TrustUI       bool   `json:"trust_ui"`
	ConfirmSource bool   `json:"confirm_source"`
}
type Manager struct {
	mu        sync.Mutex
	store     *Store
	stateRoot string
	registry  *plugin.Registry
	catalog   Catalog
	problems  map[string]string
}

func OpenManager(dir, stateRoot string, registry *plugin.Registry) (*Manager, error) {
	store, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	if registry == nil || !filepath.IsAbs(stateRoot) {
		return nil, fmt.Errorf("package manager requires registry and absolute state root")
	}
	m := &Manager{store: store, stateRoot: stateRoot, registry: registry, catalog: Catalog{Format: 1, Packages: map[string]Installed{}}, problems: map[string]string{}}
	info, err := os.Lstat(filepath.Join(dir, catalogName))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCatalogBytes {
		return nil, fmt.Errorf("invalid package catalog file")
	}
	file, err := os.Open(filepath.Join(dir, catalogName))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil || len(data) > maxCatalogBytes {
		return nil, fmt.Errorf("package catalog exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&m.catalog) != nil || d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("corrupt package catalog")
	}
	if err = m.catalog.validate(); err != nil {
		return nil, err
	}
	return m, nil
}
func (c Catalog) validate() error {
	if c.Format != 1 || c.Packages == nil || len(c.Packages) > MaxInstalledPackages {
		return fmt.Errorf("invalid package catalog")
	}
	for id, entry := range c.Packages {
		if !packageName.MatchString(id) || entry.ID != id || len(entry.Versions) == 0 || len(entry.Versions) > MaxPackageRevisions {
			return fmt.Errorf("invalid package catalog entry")
		}
		seen := map[string]bool{}
		found := false
		for _, v := range entry.Versions {
			if !packageRevision.MatchString(v.Revision) || seen[v.Revision] || v.Source.validate() != nil {
				return fmt.Errorf("invalid package version receipt")
			}
			seen[v.Revision] = true
			if v.Revision == entry.Current {
				found = true
				if entry.Enabled && (!v.Reviewed || entry.Removed) {
					return fmt.Errorf("package enabled without a reviewed version")
				}
			}
		}
		if !found {
			return fmt.Errorf("package current revision is unknown")
		}
	}
	return nil
}
func cloneCatalog(c Catalog) Catalog {
	raw, _ := json.Marshal(c)
	var next Catalog
	_ = json.Unmarshal(raw, &next)
	return next
}
func (m *Manager) save(next Catalog) error {
	if err := next.validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > maxCatalogBytes {
		return fmt.Errorf("package catalog limit reached")
	}
	return atomicfile.WriteFile(filepath.Join(m.store.dir, catalogName), append(raw, '\n'), 0600)
}
func (m *Manager) List() []Installed {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := cloneCatalog(m.catalog)
	out := make([]Installed, 0, len(copy.Packages))
	for id, entry := range copy.Packages {
		entry.UpdateRuntimeState(m.registry, m)
		entry.Problem = m.problems[id]
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *Manager) Install(ctx context.Context, source Source) (Version, error) {
	v, err := m.store.Stage(ctx, source)
	if err != nil {
		return Version{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return Version{}, err
	}
	next := cloneCatalog(m.catalog)
	entry, exists := next.Packages[v.Manifest.ID]
	if !exists {
		if len(next.Packages) >= MaxInstalledPackages {
			return Version{}, fmt.Errorf("installed package limit is %d", MaxInstalledPackages)
		}
		entry = Installed{ID: v.Manifest.ID, Current: v.Revision, Versions: []VersionInfo{}}
	}
	found := false
	for _, old := range entry.Versions {
		found = found || old.Revision == v.Revision
	}
	if !found {
		if len(entry.Versions) >= MaxPackageRevisions {
			return Version{}, fmt.Errorf("package revision history is full")
		}
		entry.Versions = append(entry.Versions, VersionInfo{Revision: v.Revision, Version: v.Manifest.Version, Title: v.Manifest.Title, Source: v.Source, Access: v.Manifest.AccessSummary(), StateSchema: v.Manifest.StateSchema})
	}
	entry.Removed = false
	next.Packages[entry.ID] = entry
	if err = m.save(next); err != nil {
		return Version{}, err
	}
	m.catalog = next
	delete(m.problems, entry.ID)
	return v, nil
}
func versionInfo(entry Installed, revision string) (VersionInfo, int, bool) {
	for i, v := range entry.Versions {
		if v.Revision == revision {
			return v, i, true
		}
	}
	return VersionInfo{}, 0, false
}
func sameSource(a, b Source) bool { return a.Kind == b.Kind && a.Location == b.Location }
func (m *Manager) capability(v Version) (*Capability, error) {
	id, revision := v.Manifest.ID, v.Revision
	return NewCapability(m.store, v, filepath.Join(m.stateRoot, v.Manifest.CapabilityID()), func(enabled bool) error { return m.SetEnabled(id, revision, enabled) })
}
func (m *Manager) Activate(ctx context.Context, in Activation) error {
	if !in.Confirm {
		return fmt.Errorf("review the exact package revision and confirm activation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activate(ctx, in)
}
func (m *Manager) activate(parent context.Context, in Activation) error {
	entry, ok := m.catalog.Packages[in.ID]
	if !ok || entry.Removed {
		return ErrPackageNotFound
	}
	if entry.Current != in.Expected {
		return ErrPackageConflict
	}
	selected, index, ok := versionInfo(entry, in.Revision)
	if !ok {
		return ErrPackageNotFound
	}
	current, _, _ := versionInfo(entry, entry.Current)
	if existing, exists := m.registry.Entry("pkg-" + in.ID); exists {
		if owner, owned := existing.Plugin.(*Capability); !owned || owner.store.dir != m.store.dir {
			return fmt.Errorf("package capability id is owned by another registration")
		}
	}
	if current.StateSchema != selected.StateSchema {
		return fmt.Errorf("package state schema changed; no automatic data migration is supported")
	}
	if !sameSource(current.Source, selected.Source) && !in.ConfirmSource {
		return fmt.Errorf("package source changed; explicit source confirmation is required")
	}
	v, err := m.store.Version(in.ID, in.Revision)
	if err != nil {
		return err
	}
	if v.Manifest.NeedsUITrust() && !in.TrustUI && !selected.UITrusted {
		return fmt.Errorf("this revision contains same-origin browser code; explicit UI trust is required")
	}
	candidate, err := m.capability(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err = candidate.Probe(ctx); err != nil {
		m.problems[in.ID] = err.Error()
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = m.store.Version(in.ID, in.Revision); err != nil {
		return err
	}
	next := cloneCatalog(m.catalog)
	updated := next.Packages[in.ID]
	updated.Current = in.Revision
	updated.Enabled = true
	updated.Versions[index].Reviewed = true
	updated.Versions[index].UITrusted = selected.UITrusted || in.TrustUI
	updated.Problem = ""
	next.Packages[in.ID] = updated
	if err = m.registry.Publish(candidate, plugin.StateEnabled, func() error { return m.save(next) }); err != nil {
		return err
	}
	m.catalog = next
	delete(m.problems, in.ID)
	return nil
}
func (m *Manager) SetEnabled(id, expected string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.catalog.Packages[id]
	if !ok || entry.Removed {
		return ErrPackageNotFound
	}
	if entry.Current != expected {
		return ErrPackageConflict
	}
	selected, _, _ := versionInfo(entry, entry.Current)
	if enabled {
		if !selected.Reviewed {
			return fmt.Errorf("review this package in the package manager before enabling")
		}
		return m.activate(context.Background(), Activation{ID: id, Revision: expected, Expected: expected, Confirm: true, TrustUI: selected.UITrusted})
	}
	existing, registered := m.registry.Entry("pkg-" + id)
	var candidate plugin.Plugin
	if registered {
		owner, owned := existing.Plugin.(*Capability)
		if !owned || owner.store.dir != m.store.dir {
			return fmt.Errorf("package capability id is owned by another registration")
		}
		candidate = owner
	}
	next := cloneCatalog(m.catalog)
	entry = next.Packages[id]
	entry.Enabled = false
	next.Packages[id] = entry
	if registered {
		if err := m.registry.Publish(candidate, plugin.StateDisabled, func() error { return m.save(next) }); err != nil {
			return err
		}
	} else if err := m.save(next); err != nil {
		return err
	}
	m.catalog = next
	delete(m.problems, id)
	return nil
}
func (m *Manager) Remove(id, expected string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.catalog.Packages[id]
	if !ok {
		return ErrPackageNotFound
	}
	if entry.Current != expected {
		return ErrPackageConflict
	}
	next := cloneCatalog(m.catalog)
	entry.Enabled = false
	entry.Removed = true
	next.Packages[id] = entry
	if existing, ok := m.registry.Entry("pkg-" + id); ok && m.owns(existing) {
		if err := m.registry.Remove("pkg-"+id, func() error { return m.save(next) }); err != nil {
			return err
		}
	} else if err := m.save(next); err != nil {
		return err
	}
	m.catalog = next
	delete(m.problems, id)
	return nil
}

// Restore 只重建已审阅的安装状态；一个损坏包不会阻止其它能力启动。
func (m *Manager) Restore(ctx context.Context) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	problems := []string{}
	for id, entry := range m.catalog.Packages {
		if err := ctx.Err(); err != nil {
			m.problems[id] = "package restore budget expired"
			problems = append(problems, id)
			continue
		}
		if entry.Removed {
			continue
		}
		selected, _, _ := versionInfo(entry, entry.Current)
		if !selected.Reviewed {
			continue
		}
		v, err := m.store.Version(id, entry.Current)
		if err == nil {
			var candidate *Capability
			candidate, err = m.capability(v)
			if err == nil {
				state := plugin.StateDisabled
				if entry.Enabled {
					if v.Manifest.NeedsUITrust() && !selected.UITrusted {
						err = fmt.Errorf("UI trust is missing")
					} else {
						err = candidate.Probe(ctx)
					}
					if err == nil {
						state = plugin.StateEnabled
					}
				}
				if err == nil {
					err = m.registry.Publish(candidate, state, nil)
				}
			}
		}
		if err != nil {
			m.problems[id] = err.Error()
			problems = append(problems, id)
		}
	}
	return problems
}

func (m *Manager) owns(entry plugin.Entry) bool {
	owner, ok := entry.Plugin.(*Capability)
	return ok && owner.store.dir == m.store.dir
}

func (e *Installed) UpdateRuntimeState(r *plugin.Registry, m *Manager) {
	e.RuntimeState = "installed"
	if e.Removed {
		e.RuntimeState = "removed"
		return
	}
	if entry, ok := r.Entry("pkg-" + e.ID); ok && entry.Plugin != nil && m.owns(entry) {
		e.RuntimeState = string(entry.State)
	}
}
