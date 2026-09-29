package devworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Qaraku/luna-agent/internal/release"
)

const MaxFiles = 10000
const MaxFileBytes = 16 << 20
const MaxTotalBytes = 128 << 20
const MaxMetadataBytes = 4 << 20
const MaxPatchBytes = 32 << 20

var sourcePaths = []string{"AGENTS.md", "README.md", "LICENSE", ".gitignore", ".gitattributes", ".github", "go.mod", "go.sum", "cmd", "internal", "plugins", "web", "presets", "docs"}

type File struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	Executable bool   `json:"executable"`
}

func digest(raw []byte) string          { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func fileSetDigest(files []File) string { raw, _ := json.Marshal(files); return digest(raw) }
func excluded(name string) bool {
	for _, part := range strings.Split(name, "/") {
		lower := strings.ToLower(part)
		switch lower {
		case ".git", ".runtime", ".evidence", "local-evidence", ".spec", ".hermes", "node_modules", ".cache", "spikes", ".env", "provider.yaml", "settings.yaml", "config.yaml", "memory.jsonl", "sessions", "credentials", "secrets":
			return true
		}
		if strings.HasPrefix(lower, ".env.") || strings.HasPrefix(lower, ".luna-data-") || lower == ".luna-restore-pending" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") {
			return true
		}
	}
	return false
}

type checkedReader struct {
	ctx context.Context
	r   io.Reader
}

func (r checkedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func readFile(ctx context.Context, root *os.Root, name string) ([]byte, File, error) {
	empty := File{}
	if !release.SafePath(name) || excluded(name) {
		return nil, empty, errors.New("unsafe or private development path")
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, empty, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, empty, errors.New("development links are not supported")
		}
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, empty, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return nil, empty, errors.New("development file is not bounded and regular")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, empty, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(checkedReader{ctx, f}, MaxFileBytes+1))
	if err != nil {
		return nil, empty, err
	}
	if len(raw) > MaxFileBytes || int64(len(raw)) != info.Size() {
		return nil, empty, errors.New("development file changed or exceeded its byte limit")
	}
	return raw, File{Path: name, SHA256: digest(raw), Bytes: int64(len(raw)), Executable: info.Mode().Perm()&0111 != 0}, nil
}
func scan(ctx context.Context, dir string, strict bool) ([]File, []string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	files := []File{}
	ignored := []string{}
	var total int64
	visited := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		visited++
		if visited > MaxFiles*2 {
			return errors.New("development scan entry limit exceeded")
		}
		if excluded(name) {
			if strict {
				return errors.New("baseline contains a private or generated path")
			}
			if name != ".git" {
				ignored = append(ignored, name)
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !release.SafePath(name) {
			return errors.New("invalid development path")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("development links and special files are refused")
		}
		_, f, err := readFile(ctx, root, name)
		if err != nil {
			return err
		}
		total += f.Bytes
		if total > MaxTotalBytes || len(files) >= MaxFiles {
			return errors.New("development snapshot limit exceeded")
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(ignored)
	return files, ignored, nil
}
func copyFile(ctx context.Context, source *os.Root, file File, dest string) error {
	raw, current, err := readFile(ctx, source, file.Path)
	if err != nil {
		return err
	}
	if current != file {
		return errors.New("development source changed during copying")
	}
	target := filepath.Join(dest, filepath.FromSlash(file.Path))
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if file.Executable {
		mode = 0700
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = out.Write(raw)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func readMetadata(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxMetadataBytes {
		return nil, errors.New("invalid development metadata")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxMetadataBytes {
		return nil, errors.New("development metadata exceeds limit")
	}
	return raw, nil
}
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > MaxMetadataBytes {
		return errors.New("development metadata exceeds limit")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
