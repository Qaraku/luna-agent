package packages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	catalog "github.com/Qaraku/luna-agent/internal/skills"
)

const receiptName = ".luna-receipt.json"
const MaxPackageRevisions = 32

var ErrPackageConflict = errors.New("package revision changed; refresh before applying")
var ErrPackageNotFound = errors.New("unknown package revision")

type Source struct {
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Revision string `json:"revision,omitempty"`
}
type FileDigest struct {
	Path       string `json:"path"`
	Hash       string `json:"sha256"`
	Executable bool   `json:"executable"`
	Bytes      int64  `json:"bytes"`
}
type Version struct {
	Manifest      Manifest     `json:"manifest"`
	Source        Source       `json:"source"`
	Revision      string       `json:"revision"`
	ContentDigest string       `json:"content_digest"`
	Files         []FileDigest `json:"files"`
	CreatedAt     time.Time    `json:"created_at"`
	Root          string       `json:"-"`
}
type Store struct{ dir string }
type packageFile struct {
	data       []byte
	executable bool
}

func OpenStore(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("package state root must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("package root must be a real directory")
	}
	return &Store{dir}, nil
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func revisionDigest(v Version) string {
	raw, _ := json.Marshal(struct {
		Content string
		Source  Source
	}{v.ContentDigest, v.Source})
	return digest(raw)
}
func contentDigest(m Manifest, files []FileDigest) string {
	raw, _ := json.Marshal(struct {
		Manifest Manifest
		Files    []FileDigest
	}{m, files})
	return digest(raw)
}
func (s *Store) Stage(ctx context.Context, source Source) (Version, error) {
	if err := source.validate(); err != nil {
		return Version{}, err
	}
	var m Manifest
	var files map[string]packageFile
	var err error
	if source.Kind == "local" {
		source.Location, err = filepath.Abs(source.Location)
		if err == nil {
			m, files, err = readLocalPackage(ctx, source.Location)
		}
	} else {
		m, files, err = readGitPackage(ctx, source)
	}
	if err != nil {
		return Version{}, err
	}
	return s.stageFiles(ctx, source, m, files)
}
func readLocalPackage(ctx context.Context, dir string) (Manifest, map[string]packageFile, error) {
	var empty Manifest
	info, err := os.Lstat(dir)
	if err != nil {
		return empty, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return empty, nil, fmt.Errorf("local package root must be a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return empty, nil, err
	}
	defer root.Close()
	manifest, _, err := readPackageFile(ctx, root, ManifestName, MaxManifestBytes)
	if err != nil {
		return empty, nil, err
	}
	m, err := ParseManifest(manifest)
	if err != nil {
		return empty, nil, err
	}
	files := map[string]packageFile{}
	total := int64(len(manifest))
	for _, name := range m.Files {
		if name == receiptName {
			return empty, nil, fmt.Errorf("reserved receipt file")
		}
		body, executable, err := readPackageFile(ctx, root, name, MaxPackageFileBytes)
		if err != nil {
			return empty, nil, err
		}
		total += int64(len(body))
		if total > MaxPackageBytes {
			return empty, nil, fmt.Errorf("package payload exceeds %d bytes", MaxPackageBytes)
		}
		files[name] = packageFile{body, executable}
	}
	return m, files, nil
}
func readPackageFile(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("package symbolic links are not allowed")
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, false, fmt.Errorf("package file is not regular or exceeds its byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, false, fmt.Errorf("package file grew beyond its byte limit")
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	return data, info.Mode().Perm()&0111 != 0, nil
}
func (s *Store) stageFiles(ctx context.Context, source Source, m Manifest, files map[string]packageFile) (Version, error) {
	if err := m.Validate(); err != nil {
		return Version{}, err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Version{}, err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxManifestBytes {
		return Version{}, fmt.Errorf("normalized manifest exceeds %d bytes", MaxManifestBytes)
	}
	total := int64(len(raw))
	for _, name := range m.Files {
		file, ok := files[name]
		if !ok || len(file.data) > MaxPackageFileBytes {
			return Version{}, fmt.Errorf("declared package file is missing or exceeds byte limit")
		}
		total += int64(len(file.data))
		if total > MaxPackageBytes {
			return Version{}, fmt.Errorf("normalized package payload exceeds %d bytes", MaxPackageBytes)
		}
	}
	for _, dir := range m.Skills {
		if _, err := catalog.ParseDefinition(filepath.Base(dir), dir, files[dir+"/SKILL.md"].data); err != nil {
			return Version{}, fmt.Errorf("package skill: %w", err)
		}
	}
	for _, tool := range m.Tools {
		if tool.Interpreter == "" && !files[tool.Entry].executable {
			return Version{}, fmt.Errorf("tool entry must be executable or use a supported interpreter")
		}
	}
	names := append([]string{}, m.Files...)
	sort.Strings(names)
	v := Version{Manifest: m, Source: source, CreatedAt: time.Now().UTC(), Files: []FileDigest{}}
	for _, name := range names {
		file, ok := files[name]
		if !ok {
			return Version{}, fmt.Errorf("declared package file is missing")
		}
		v.Files = append(v.Files, FileDigest{Path: name, Hash: digest(file.data), Executable: file.executable, Bytes: int64(len(file.data))})
	}
	v.ContentDigest = contentDigest(m, v.Files)
	v.Revision = revisionDigest(v)
	if existing, err := s.Version(m.ID, v.Revision); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrPackageNotFound) {
		return Version{}, err
	}
	parent := filepath.Join(s.dir, "versions", m.ID)
	if err := safePackageDirectories(s.dir, []string{"versions", m.ID}); err != nil {
		return Version{}, err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return Version{}, err
	}
	if len(entries) >= MaxPackageRevisions {
		return Version{}, fmt.Errorf("package revision limit is %d; retained revisions were not deleted", MaxPackageRevisions)
	}
	stage, err := os.MkdirTemp(s.dir, ".stage-")
	if err != nil {
		return Version{}, err
	}
	defer os.RemoveAll(stage)
	if err = os.WriteFile(filepath.Join(stage, ManifestName), raw, 0600); err != nil {
		return Version{}, err
	}
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return Version{}, err
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return Version{}, err
		}
		mode := os.FileMode(0600)
		if files[name].executable {
			mode = 0700
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return Version{}, err
		}
		_, writeErr := f.Write(files[name].data)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return Version{}, writeErr
		}
		if closeErr != nil {
			return Version{}, closeErr
		}
	}
	receipt, _ := json.Marshal(v)
	if err = os.WriteFile(filepath.Join(stage, receiptName), append(receipt, '\n'), 0600); err != nil {
		return Version{}, err
	}
	if err = ctx.Err(); err != nil {
		return Version{}, err
	}
	target := filepath.Join(parent, v.Revision)
	if err = os.Rename(stage, target); err != nil {
		if existing, verifyErr := s.Version(m.ID, v.Revision); verifyErr == nil {
			return existing, nil
		}
		return Version{}, err
	}
	v.Root = target
	return v, nil
}
func safePackageDirectories(root string, parts []string) error {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer opened.Close()
	path := ""
	for _, part := range parts {
		path = filepath.Join(path, part)
		if err = opened.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := opened.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("package state directory must not be a link")
		}
	}
	return nil
}
func (s *Store) Version(id, revision string) (Version, error) {
	var v Version
	if !packageName.MatchString(id) || !packageRevision.MatchString(revision) {
		return v, ErrPackageNotFound
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return v, err
	}
	defer root.Close()
	relative := filepath.Join("versions", id, revision)
	for _, part := range []string{"versions", filepath.Join("versions", id), relative} {
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return v, ErrPackageNotFound
		}
		if err != nil {
			return v, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return v, fmt.Errorf("package revision directory is invalid")
		}
	}
	versionRoot, err := root.OpenRoot(relative)
	if err != nil {
		return v, err
	}
	defer versionRoot.Close()
	raw, _, err := readPackageFile(context.Background(), versionRoot, receiptName, MaxManifestBytes*4)
	if err != nil {
		return v, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&v); err != nil {
		return v, fmt.Errorf("invalid package receipt")
	}
	if v.Manifest.ID != id || v.Revision != revision || v.Source.validate() != nil || v.Manifest.Validate() != nil || len(v.Files) != len(v.Manifest.Files) || v.ContentDigest != contentDigest(v.Manifest, v.Files) || v.Revision != revisionDigest(v) {
		return Version{}, fmt.Errorf("package receipt integrity mismatch")
	}
	manifest, _, err := readPackageFile(context.Background(), versionRoot, ManifestName, MaxManifestBytes)
	if err != nil {
		return v, err
	}
	parsed, err := ParseManifest(manifest)
	if err != nil {
		return v, err
	}
	canonical, _ := json.Marshal(parsed)
	expected, _ := json.Marshal(v.Manifest)
	if !bytes.Equal(canonical, expected) {
		return v, fmt.Errorf("installed package manifest changed")
	}
	seen := map[string]bool{}
	total := int64(len(manifest))
	for _, file := range v.Files {
		if !safePackagePath(file.Path) || seen[file.Path] {
			return v, fmt.Errorf("invalid package file receipt")
		}
		seen[file.Path] = true
		body, executable, err := readPackageFile(context.Background(), versionRoot, file.Path, MaxPackageFileBytes)
		if err != nil {
			return v, err
		}
		total += int64(len(body))
		if total > MaxPackageBytes || file.Hash != digest(body) || file.Bytes != int64(len(body)) || file.Executable != executable {
			return v, fmt.Errorf("installed package content changed")
		}
	}
	for _, name := range v.Manifest.Files {
		if !seen[name] {
			return v, fmt.Errorf("package file receipt is incomplete")
		}
	}
	v.Root = filepath.Join(s.dir, relative)
	return v, nil
}
