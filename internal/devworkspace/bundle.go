package devworkspace

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Qaraku/luna-agent/internal/datalifecycle"
	"github.com/Qaraku/luna-agent/internal/release"
	"github.com/Qaraku/luna-agent/internal/sourcecopy"
)

type Bundle struct {
	Format         int       `json:"format"`
	SourceCommit   string    `json:"source_commit"`
	BaselineDigest string    `json:"baseline_digest"`
	CreatedAt      time.Time `json:"created_at"`
	PatchSHA256    string    `json:"patch_sha256"`
	PatchBytes     int64     `json:"patch_bytes"`
	Changes        []Change  `json:"changes"`
	Excluded       []string  `json:"excluded,omitempty"`
}

func validFile(file *File, name string) bool {
	if file == nil {
		return true
	}
	sum, err := hex.DecodeString(file.SHA256)
	return err == nil && len(sum) == 32 && file.Path == name && file.Bytes >= 0 && file.Bytes <= MaxFileBytes
}
func (b Bundle) validate() error {
	hash, err := hex.DecodeString(b.PatchSHA256)
	baseline, baseErr := hex.DecodeString(b.BaselineDigest)
	if b.Format != 1 || !sourcecopy.CommitPattern.MatchString(b.SourceCommit) || b.CreatedAt.IsZero() || err != nil || len(hash) != 32 || baseErr != nil || len(baseline) != 32 || b.PatchBytes < 1 || b.PatchBytes > MaxPatchBytes || len(b.Changes) == 0 || len(b.Changes) > MaxFiles || len(b.Excluded) > MaxFiles*2 {
		return errors.New("invalid development bundle metadata")
	}
	last := ""
	for _, c := range b.Changes {
		if !release.SafePath(c.Path) || excluded(c.Path) || c.Path <= last || !validFile(c.Before, c.Path) || !validFile(c.After, c.Path) {
			return errors.New("invalid development change")
		}
		last = c.Path
		switch c.Kind {
		case "added":
			if c.Before != nil || c.After == nil {
				return errors.New("invalid addition")
			}
		case "deleted":
			if c.Before == nil || c.After != nil {
				return errors.New("invalid deletion")
			}
		case "modified":
			if c.Before == nil || c.After == nil || *c.Before == *c.After {
				return errors.New("invalid modification")
			}
		default:
			return errors.New("unknown development change")
		}
	}
	for _, name := range b.Excluded {
		if !release.SafePath(name) {
			return errors.New("invalid excluded path metadata")
		}
	}
	return nil
}
func Export(ctx context.Context, workspace, dest string, include []string) (b Bundle, err error) {
	workspace, err = datalifecycle.Canonical(workspace)
	if err != nil {
		return b, err
	}
	dest, err = datalifecycle.Canonical(dest)
	if err != nil {
		return b, err
	}
	if !outside(dest, workspace) {
		return b, errors.New("candidate bundle must be outside the development workspace")
	}
	view, err := Status(ctx, workspace)
	if err != nil {
		return b, err
	}
	if !outside(dest, view.workspace.SourceRepository) {
		return b, errors.New("candidate bundle must be outside the original repository")
	}
	current := map[string]File{}
	for _, file := range view.files {
		current[file.Path] = file
	}
	selected := map[string]bool{}
	for _, file := range view.workspace.Files {
		selected[file.Path] = true
	}
	untracked := map[string]bool{}
	for _, name := range view.Untracked {
		untracked[name] = true
	}
	seen := map[string]bool{}
	for _, name := range include {
		if !release.SafePath(name) || excluded(name) || seen[name] || !untracked[name] {
			return b, errors.New("include must name distinct new, non-private regular files from dev status")
		}
		seen[name] = true
		selected[name] = true
		file := current[name]
		view.Changes = append(view.Changes, Change{Path: name, Kind: "added", After: &file})
	}
	if len(seen) != len(untracked) {
		return b, errors.New("new source files remain unselected; inspect dev status and add each with -include")
	}
	if len(view.Changes) == 0 {
		return b, errors.New("development workspace has no code changes")
	}
	sort.Slice(view.Changes, func(i, j int) bool { return view.Changes[i].Path < view.Changes[j].Path })
	temporary, err := os.MkdirTemp("", "luna-patch-")
	if err != nil {
		return b, err
	}
	defer os.RemoveAll(temporary)
	before, after := filepath.Join(temporary, "before"), filepath.Join(temporary, "after")
	if err = os.Mkdir(before, 0700); err != nil {
		return b, err
	}
	if err = os.Mkdir(after, 0700); err != nil {
		return b, err
	}
	baselineRoot, err := os.OpenRoot(filepath.Join(workspace, "baseline"))
	if err != nil {
		return b, err
	}
	defer baselineRoot.Close()
	for _, file := range view.workspace.Files {
		if err = copyFile(ctx, baselineRoot, file, before); err != nil {
			return b, err
		}
	}
	if err = initGit(ctx, before); err != nil {
		return b, err
	}
	sourceRoot, err := os.OpenRoot(filepath.Join(workspace, "source"))
	if err != nil {
		return b, err
	}
	defer sourceRoot.Close()
	for _, file := range view.files {
		if selected[file.Path] {
			if err = copyFile(ctx, sourceRoot, file, after); err != nil {
				return b, err
			}
		}
	}
	locations := []string{"--git-dir=" + filepath.Join(before, ".git"), "--work-tree=" + after}
	if _, err = git(ctx, after, 1024, append(append([]string{}, locations...), "add", "-A", "-f", "--", ".")...); err != nil {
		return b, err
	}
	patch, err := git(ctx, after, MaxPatchBytes, append(locations, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "HEAD", "--")...)
	if err != nil {
		return b, err
	}
	again, err := Status(ctx, workspace)
	if err != nil {
		return b, err
	}
	if fileSetDigest(again.files) != fileSetDigest(view.files) {
		return b, errors.New("development files changed during patch export")
	}
	b = Bundle{Format: 1, SourceCommit: view.SourceCommit, BaselineDigest: view.workspace.BaselineDigest, CreatedAt: time.Now().UTC(), PatchSHA256: digest(patch), PatchBytes: int64(len(patch)), Changes: view.Changes, Excluded: view.Excluded}
	if err = b.validate(); err != nil {
		return b, err
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		return b, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dest)
		}
	}()
	if err = os.WriteFile(filepath.Join(dest, PatchName), patch, 0600); err != nil {
		return b, err
	}
	if err = writeJSON(filepath.Join(dest, BundleName), b); err != nil {
		return b, err
	}
	if err = ctx.Err(); err != nil {
		return b, err
	}
	ok = true
	return b, nil
}
func Inspect(ctx context.Context, dir string) (b Bundle, err error) {
	raw, err := readMetadata(dir, BundleName)
	if err != nil {
		return b, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&b) != nil || decoder.Decode(new(any)) != io.EOF {
		return b, errors.New("invalid development bundle")
	}
	if err = b.validate(); err != nil {
		return b, err
	}
	opened, err := os.OpenRoot(dir)
	if err != nil {
		return b, err
	}
	defer opened.Close()
	info, err := opened.Lstat(PatchName)
	if err != nil {
		return b, err
	}
	if !info.Mode().IsRegular() || info.Size() != b.PatchBytes {
		return b, errors.New("invalid candidate patch file")
	}
	file, err := opened.Open(PatchName)
	if err != nil {
		return b, err
	}
	defer file.Close()
	patch, err := io.ReadAll(io.LimitReader(checkedReader{ctx, file}, MaxPatchBytes+1))
	if err != nil {
		return b, err
	}
	if int64(len(patch)) != b.PatchBytes || digest(patch) != b.PatchSHA256 {
		return b, fmt.Errorf("candidate patch integrity mismatch")
	}
	return b, nil
}
