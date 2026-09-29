package skills

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Qaraku/luna-agent/internal/fileread"
	catalog "github.com/Qaraku/luna-agent/internal/skills"
	yaml "gopkg.in/yaml.v3"
)

const MaxLearnedSkills = 64
const MaxSkillRevisions = 32
const MaxSkillBodyBytes = 32 * 1024
const MaxSkillBundleBytes = 128 * 1024
const maxSkillJournalBytes = 1024 * 1024
const maxSkillDiffBytes = 12 * 1024

var learnedName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var referenceName = regexp.MustCompile(`^references/[a-z0-9][a-z0-9_.-]{0,79}[.]md$`)
var revisionName = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrLibraryNotFound = errors.New("unknown learned skill or revision")
var ErrLibraryConflict = errors.New("skill changed; refresh its current revision before saving")
var ErrLibraryCorrupt = errors.New("corrupt skill revision or library")

type SkillDefinition struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Body        string            `json:"body"`
	Files       map[string]string `json:"files,omitempty"`
}
type SkillOrigin struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type SkillRevision struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Revision    string            `json:"revision"`
	Parent      string            `json:"parent,omitempty"`
	Sequence    int               `json:"sequence"`
	At          time.Time         `json:"at"`
	Origin      SkillOrigin       `json:"origin"`
	Files       map[string]string `json:"files"`
	Nonce       string            `json:"nonce"`
}
type SkillPreview struct {
	Name             string `json:"name"`
	ExpectedRevision string `json:"expected_revision"`
	Diff             string `json:"diff"`
	Truncated        bool   `json:"truncated"`
}
type Library struct {
	mu  sync.Mutex
	dir string
}

func OpenLibrary(dir string) (*Library, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("skill library root must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("skill library root must be a real directory")
	}
	lib := &Library{dir: dir}
	if _, err = lib.List(); err != nil {
		return nil, err
	}
	return lib, nil
}
func (l *Library) Directory(e SkillRevision) string {
	return filepath.Join(l.dir, "entries", e.Revision, e.Name)
}
func textOK(s string, limit int) bool {
	return utf8.ValidString(s) && len(s) <= limit && !strings.ContainsRune(s, 0)
}
func (d SkillDefinition) validate() error {
	if !learnedName.MatchString(d.Name) {
		return fmt.Errorf("invalid learned skill name")
	}
	if !textOK(d.Description, 4096) || strings.TrimSpace(d.Description) == "" || utf8.RuneCountInString(d.Description) > 1024 {
		return fmt.Errorf("skill description must contain 1..1024 characters")
	}
	if !textOK(d.Body, MaxSkillBodyBytes) || strings.TrimSpace(d.Body) == "" {
		return fmt.Errorf("skill body must be nonempty text within %d bytes", MaxSkillBodyBytes)
	}
	if len(d.Files) > 8 {
		return fmt.Errorf("skill supports at most eight Markdown reference files")
	}
	total := len(d.Body) + len(d.Description)
	for name, body := range d.Files {
		if !referenceName.MatchString(name) || !textOK(body, MaxSkillBodyBytes) {
			return fmt.Errorf("reference files must be bounded Markdown under references/")
		}
		total += len(body)
	}
	if total > MaxSkillBundleBytes {
		return fmt.Errorf("skill bundle exceeds %d bytes", MaxSkillBundleBytes)
	}
	return nil
}
func canonicalFiles(d SkillDefinition) (map[string]string, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	head, err := yaml.Marshal(struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}{d.Name, d.Description})
	if err != nil {
		return nil, err
	}
	out := map[string]string{catalog.FileName: "---\n" + string(head) + "---\n" + d.Body}
	for path, body := range d.Files {
		out[path] = body
	}
	return out, nil
}
func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func skillRevisionDigest(e SkillRevision) string {
	e.Revision = ""
	data, _ := json.Marshal(e)
	return hashText(string(data))
}
func validOrigin(o SkillOrigin) bool {
	return (o.Kind == "user" || o.Kind == "model" || o.Kind == "import") && textOK(o.Reason, 1024) && textOK(o.SessionID, 128) && textOK(o.RunID, 128)
}
func (l *Library) history(name string) ([]SkillRevision, error) {
	if !learnedName.MatchString(name) {
		return nil, fmt.Errorf("invalid skill name")
	}
	root, err := os.OpenRoot(l.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name + ".jsonl")
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrLibraryNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrLibraryCorrupt
	}
	f, err := root.Open(name + ".jsonl")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSkillJournalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxSkillJournalBytes || raw[len(raw)-1] != '\n' {
		return nil, ErrLibraryCorrupt
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	if len(lines) > MaxSkillRevisions {
		return nil, ErrLibraryCorrupt
	}
	history := make([]SkillRevision, 0, len(lines))
	parent := ""
	for i, line := range lines {
		var e SkillRevision
		if json.Unmarshal(line, &e) != nil || e.Name != name || e.Sequence != i+1 || e.Parent != parent || !revisionName.MatchString(e.Revision) || e.Revision != skillRevisionDigest(e) || !validOrigin(e.Origin) || len(e.Files) < 1 || len(e.Files) > 9 || !revisionName.MatchString(e.Files[catalog.FileName]) {
			return nil, ErrLibraryCorrupt
		}
		for path, digest := range e.Files {
			if (path != catalog.FileName && !referenceName.MatchString(path)) || !revisionName.MatchString(digest) {
				return nil, ErrLibraryCorrupt
			}
		}
		parent = e.Revision
		history = append(history, e)
	}
	return history, nil
}
func (l *Library) History(name string) ([]SkillRevision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.history(name)
}
func (l *Library) lookup(name, revision string) (SkillRevision, error) {
	history, err := l.history(name)
	if err != nil {
		return SkillRevision{}, err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if revision == "" || history[i].Revision == revision {
			return history[i], nil
		}
	}
	return SkillRevision{}, ErrLibraryNotFound
}
func (l *Library) Lookup(name, revision string) (SkillRevision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lookup(name, revision)
}
func (l *Library) list() ([]SkillRevision, error) {
	root, err := os.OpenRoot(l.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(MaxLearnedSkills + 2)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > MaxLearnedSkills+1 {
		return nil, fmt.Errorf("skill library entry limit exceeded")
	}
	out := []SkillRevision{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		history, err := l.history(strings.TrimSuffix(entry.Name(), ".jsonl"))
		if err != nil {
			return nil, err
		}
		out = append(out, history[len(history)-1])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (l *Library) List() ([]SkillRevision, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.list() }
func (l *Library) definition(e SkillRevision) (SkillDefinition, error) {
	dir, err := l.SafeDirectory(e)
	if err != nil {
		return SkillDefinition{}, err
	}
	d := SkillDefinition{Name: e.Name, Description: e.Description, Files: map[string]string{}}
	for path, digest := range e.Files {
		resolved, err := fileread.Resolve(dir, path, MaxSkillBundleBytes)
		if err != nil {
			return SkillDefinition{}, fmt.Errorf("%w: %v", ErrLibraryCorrupt, err)
		}
		body, err := fileread.Read(resolved, MaxSkillBundleBytes)
		if err != nil || hashText(body) != digest {
			return SkillDefinition{}, ErrLibraryCorrupt
		}
		if path == catalog.FileName {
			d.Body = catalog.Body(body)
		} else {
			d.Files[path] = body
		}
	}
	if err := d.validate(); err != nil {
		return SkillDefinition{}, ErrLibraryCorrupt
	}
	return d, nil
}
func (l *Library) Definition(name, revision string) (SkillDefinition, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.lookup(name, revision)
	if err != nil {
		return SkillDefinition{}, err
	}
	return l.definition(e)
}
func (l *Library) Verify(name, revision, path, body string) error {
	e, err := l.Lookup(name, revision)
	if err != nil {
		return err
	}
	digest, ok := e.Files[filepath.ToSlash(filepath.Clean(path))]
	if !ok || hashText(body) != digest {
		return ErrLibraryCorrupt
	}
	return nil
}
func (l *Library) Preview(d SkillDefinition, expected string) (SkillPreview, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.preview(d, expected)
}
func (l *Library) preview(d SkillDefinition, expected string) (SkillPreview, error) {
	files, err := canonicalFiles(d)
	if err != nil {
		return SkillPreview{}, err
	}
	old, err := l.lookup(d.Name, "")
	previous := map[string]string{}
	if err != nil && !errors.Is(err, ErrLibraryNotFound) {
		return SkillPreview{}, err
	}
	if err == nil {
		if old.Revision != expected {
			return SkillPreview{}, ErrLibraryConflict
		}
		definition, err := l.definition(old)
		if err != nil {
			return SkillPreview{}, err
		}
		previous, err = canonicalFiles(definition)
		if err != nil {
			return SkillPreview{}, err
		}
	} else if expected != "" {
		return SkillPreview{}, ErrLibraryConflict
	}
	diff := skillDiff(previous, files)
	truncated := len(diff) > maxSkillDiffBytes
	if truncated {
		cut := strings.LastIndexByte(diff[:maxSkillDiffBytes], '\n')
		if cut < 0 {
			cut = 0
		}
		diff = diff[:cut] + fmt.Sprintf("\n… diff truncated at %d bytes; export both revisions for the full content.\n", maxSkillDiffBytes)
	}
	return SkillPreview{Name: d.Name, ExpectedRevision: expected, Diff: diff, Truncated: truncated}, nil
}
func skillDiff(before, after map[string]string) string {
	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}
	keys := make([]string, 0, len(names))
	for n := range names {
		keys = append(keys, n)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, name := range keys {
		if before[name] == after[name] {
			continue
		}
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", name, name)
		a, b := strings.SplitAfter(before[name], "\n"), strings.SplitAfter(after[name], "\n")
		start := 0
		for start < len(a) && start < len(b) && a[start] == b[start] {
			start++
		}
		end := 0
		for end < len(a)-start && end < len(b)-start && a[len(a)-1-end] == b[len(b)-1-end] {
			end++
		}
		for _, line := range a[start : len(a)-end] {
			if line != "" {
				out.WriteString("-" + line)
				if !strings.HasSuffix(line, "\n") {
					out.WriteByte('\n')
				}
			}
		}
		for _, line := range b[start : len(b)-end] {
			if line != "" {
				out.WriteString("+" + line)
				if !strings.HasSuffix(line, "\n") {
					out.WriteByte('\n')
				}
			}
		}
	}
	return out.String()
}
func (l *Library) Save(d SkillDefinition, expected string, origin SkillOrigin) (SkillRevision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.save(context.Background(), d, expected, origin)
}
func (l *Library) SaveContext(ctx context.Context, d SkillDefinition, expected string, origin SkillOrigin) (SkillRevision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.save(ctx, d, expected, origin)
}

func (l *Library) save(ctx context.Context, d SkillDefinition, expected string, origin SkillOrigin) (SkillRevision, error) {
	if err := ctx.Err(); err != nil {
		return SkillRevision{}, err
	}
	if !validOrigin(origin) {
		return SkillRevision{}, fmt.Errorf("invalid skill origin")
	}
	if _, err := l.preview(d, expected); err != nil {
		return SkillRevision{}, err
	}
	history, err := l.history(d.Name)
	if err != nil && !errors.Is(err, ErrLibraryNotFound) {
		return SkillRevision{}, err
	}
	if len(history) >= MaxSkillRevisions {
		return SkillRevision{}, fmt.Errorf("skill revision limit is %d", MaxSkillRevisions)
	}
	if len(history) == 0 {
		all, err := l.list()
		if err != nil {
			return SkillRevision{}, err
		}
		if len(all) >= MaxLearnedSkills {
			return SkillRevision{}, fmt.Errorf("learned skill limit is %d", MaxLearnedSkills)
		}
	}
	files, err := canonicalFiles(d)
	if err != nil {
		return SkillRevision{}, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return SkillRevision{}, err
	}
	entry := SkillRevision{Name: d.Name, Description: d.Description, Parent: expected, Sequence: len(history) + 1, Origin: origin, At: time.Now().UTC(), Nonce: hex.EncodeToString(nonce), Files: map[string]string{}}
	for name, body := range files {
		entry.Files[name] = hashText(body)
	}
	entry.Revision = skillRevisionDigest(entry)
	if err := ctx.Err(); err != nil {
		return SkillRevision{}, err
	}
	root, err := os.OpenRoot(l.dir)
	if err != nil {
		return SkillRevision{}, err
	}
	defer root.Close()
	for _, path := range []string{"entries"} {
		if err := root.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return SkillRevision{}, err
		}
		info, err := root.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return SkillRevision{}, ErrLibraryCorrupt
		}
	}
	relative := filepath.Join("entries", entry.Revision)
	if err = root.Mkdir(relative, 0700); err != nil {
		return SkillRevision{}, err
	}
	snapshotRoot, err := root.OpenRoot(relative)
	if err != nil {
		return SkillRevision{}, err
	}
	defer snapshotRoot.Close()
	if err := snapshotRoot.Mkdir(d.Name, 0700); err != nil {
		return SkillRevision{}, err
	}
	revisionRoot, err := snapshotRoot.OpenRoot(d.Name)
	if err != nil {
		return SkillRevision{}, err
	}
	defer revisionRoot.Close()
	published := false
	defer func() {
		if !published {
			for path := range files {
				_ = revisionRoot.Remove(path)
			}
			_ = revisionRoot.Remove("references")
			_ = snapshotRoot.Remove(d.Name)
			_ = root.Remove(relative)
		}
	}()
	if len(d.Files) > 0 {
		if err = revisionRoot.Mkdir("references", 0700); err != nil {
			return SkillRevision{}, err
		}
	}
	for name, body := range files {
		if err := ctx.Err(); err != nil {
			return SkillRevision{}, err
		}
		f, err := revisionRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return SkillRevision{}, err
		}
		_, writeErr := io.WriteString(f, body)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return SkillRevision{}, writeErr
		}
		if closeErr != nil {
			return SkillRevision{}, closeErr
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return SkillRevision{}, err
	}
	raw = append(raw, '\n')
	if err := ctx.Err(); err != nil {
		return SkillRevision{}, err
	}
	flags := os.O_RDWR | os.O_APPEND
	if len(history) == 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := root.OpenFile(d.Name+".jsonl", flags, 0600)
	if err != nil {
		return SkillRevision{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return SkillRevision{}, err
	}
	if !info.Mode().IsRegular() || info.Size()+int64(len(raw)) > maxSkillJournalBytes {
		return SkillRevision{}, fmt.Errorf("skill journal limit exceeded")
	}
	if _, err = f.Write(raw); err != nil {
		return SkillRevision{}, err
	}
	published = true
	if err = f.Sync(); err != nil {
		return SkillRevision{}, err
	}
	return entry, nil
}
func (l *Library) Restore(name, revision, expected string, origin SkillOrigin) (SkillRevision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.lookup(name, revision)
	if err != nil {
		return SkillRevision{}, err
	}
	definition, err := l.definition(entry)
	if err != nil {
		return SkillRevision{}, err
	}
	return l.save(context.Background(), definition, expected, origin)
}

func (l *Library) SafeDirectory(e SkillRevision) (string, error) {
	if !learnedName.MatchString(e.Name) || !revisionName.MatchString(e.Revision) {
		return "", ErrLibraryCorrupt
	}
	dir, err := fileread.ResolveDir(l.dir, filepath.Join("entries", e.Revision, e.Name))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLibraryCorrupt, err)
	}
	return dir, nil
}
