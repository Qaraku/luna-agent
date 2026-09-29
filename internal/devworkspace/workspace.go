// Package devworkspace 建立独立核心开发副本和可审阅候选，不修改原程序或授权。
package devworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/datalifecycle"
	"github.com/Qaraku/luna-agent/internal/release"
	"github.com/Qaraku/luna-agent/internal/sourcecopy"
)

const WorkspaceName = "workspace.json"
const PatchName = "candidate.patch"
const BundleName = "candidate.json"

type Workspace struct {
	Format           int       `json:"format"`
	SourceRepository string    `json:"source_repository"`
	SourceCommit     string    `json:"source_commit"`
	CreatedAt        time.Time `json:"created_at"`
	Files            []File    `json:"files"`
	BaselineDigest   string    `json:"baseline_digest"`
}

func validateFiles(files []File) error {
	if len(files) == 0 || len(files) > MaxFiles {
		return errors.New("invalid development file count")
	}
	last := ""
	var total int64
	for _, f := range files {
		if !release.SafePath(f.Path) || excluded(f.Path) || f.Path <= last || len(f.SHA256) != 64 || f.Bytes < 0 || f.Bytes > MaxFileBytes {
			return errors.New("invalid development file declaration")
		}
		last = f.Path
		total += f.Bytes
	}
	if total > MaxTotalBytes {
		return errors.New("development files exceed byte limit")
	}
	return nil
}
func (w Workspace) validate() error {
	if w.Format != 1 || !filepath.IsAbs(w.SourceRepository) || !sourcecopy.CommitPattern.MatchString(w.SourceCommit) || w.CreatedAt.IsZero() || w.BaselineDigest != fileSetDigest(w.Files) {
		return errors.New("invalid development workspace identity")
	}
	return validateFiles(w.Files)
}
func outside(path, ancestor string) bool {
	relative, err := filepath.Rel(ancestor, path)
	return err == nil && (relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
func Create(ctx context.Context, repository, commit, dest string) (w Workspace, err error) {
	if !sourcecopy.CommitPattern.MatchString(commit) {
		return w, errors.New("development source must name a full commit")
	}
	repository, err = datalifecycle.Canonical(repository)
	if err != nil {
		return w, err
	}
	dest, err = datalifecycle.Canonical(dest)
	if err != nil {
		return w, err
	}
	if !outside(dest, repository) {
		return w, errors.New("development destination must be outside the original repository")
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		return w, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dest)
		}
	}()
	baseline, source := filepath.Join(dest, "baseline"), filepath.Join(dest, "source")
	if err = os.Mkdir(baseline, 0700); err != nil {
		return w, err
	}
	if err = os.Mkdir(source, 0700); err != nil {
		return w, err
	}
	if err = sourcecopy.ExportFiltered(ctx, repository, commit, baseline, sourcePaths, func(name string) bool { return !excluded(name) }); err != nil {
		return w, err
	}
	files, _, err := scan(ctx, baseline, true)
	if err != nil {
		return w, err
	}
	w = Workspace{Format: 1, SourceRepository: repository, SourceCommit: commit, CreatedAt: time.Now().UTC(), Files: files, BaselineDigest: fileSetDigest(files)}
	if err = w.validate(); err != nil {
		return w, err
	}
	opened, err := os.OpenRoot(baseline)
	if err != nil {
		return w, err
	}
	defer opened.Close()
	for _, file := range files {
		if err = copyFile(ctx, opened, file, source); err != nil {
			return w, err
		}
	}
	if err = initGit(ctx, source); err != nil {
		return w, err
	}
	if err = writeJSON(filepath.Join(dest, WorkspaceName), w); err != nil {
		return w, err
	}
	ok = true
	return w, nil
}
func readWorkspace(ctx context.Context, dir string) (Workspace, error) {
	var w Workspace
	raw, err := readMetadata(dir, WorkspaceName)
	if err != nil {
		return w, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&w) != nil || decoder.Decode(new(any)) != io.EOF {
		return w, errors.New("invalid development workspace metadata")
	}
	if err = w.validate(); err != nil {
		return w, err
	}
	for _, name := range []string{"source", "baseline"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.IsDir() {
			return w, errors.New("development source and baseline must be real directories")
		}
	}
	actual, _, err := scan(ctx, filepath.Join(dir, "baseline"), true)
	if err != nil {
		return w, err
	}
	if fileSetDigest(actual) != w.BaselineDigest {
		return w, errors.New("development baseline integrity mismatch")
	}
	return w, nil
}

type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Before *File  `json:"before,omitempty"`
	After  *File  `json:"after,omitempty"`
}
type View struct {
	SourceCommit string   `json:"source_commit"`
	Changes      []Change `json:"changes"`
	Untracked    []string `json:"untracked"`
	Excluded     []string `json:"excluded"`
	files        []File
	workspace    Workspace
}

func Status(ctx context.Context, dir string) (View, error) {
	w, err := readWorkspace(ctx, dir)
	if err != nil {
		return View{}, err
	}
	files, ignored, err := scan(ctx, filepath.Join(dir, "source"), false)
	if err != nil {
		return View{}, err
	}
	view := View{SourceCommit: w.SourceCommit, Changes: []Change{}, Untracked: []string{}, Excluded: ignored, files: files, workspace: w}
	base, current := map[string]File{}, map[string]File{}
	for _, file := range w.Files {
		base[file.Path] = file
	}
	for _, file := range files {
		current[file.Path] = file
	}
	for _, before := range w.Files {
		after, exists := current[before.Path]
		if !exists {
			copy := before
			view.Changes = append(view.Changes, Change{Path: before.Path, Kind: "deleted", Before: &copy})
		} else if after != before {
			b, a := before, after
			view.Changes = append(view.Changes, Change{Path: before.Path, Kind: "modified", Before: &b, After: &a})
		}
	}
	for _, file := range files {
		if _, exists := base[file.Path]; !exists {
			view.Untracked = append(view.Untracked, file.Path)
		}
	}
	sort.Slice(view.Changes, func(i, j int) bool { return view.Changes[i].Path < view.Changes[j].Path })
	return view, nil
}
