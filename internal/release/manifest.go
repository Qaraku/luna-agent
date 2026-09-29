// Package release 管理离线程序分发；它不读取私人数据，不执行更新脚本。
package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
)

const ManifestName = "release.json"
const MaxManifestBytes = 256 << 10
const MaxFileBytes = 128 << 20
const MaxTotalBytes = 512 << 20
const MaxFiles = 1024

var commitPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)
var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,95}$`)

type File struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	Executable bool   `json:"executable"`
}
type Manifest struct {
	Format     int    `json:"format"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	DataSchema int    `json:"data_schema"`
	Files      []File `json:"files"`
}

func SafePath(name string) bool {
	return name != "" && name != "." && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\:\x00\r\n") && len(name) <= 1024
}
func allowedFile(name string) bool {
	return name == "bin/luna" || name == "LICENSE" || strings.HasPrefix(name, "web/") || strings.HasPrefix(name, "plugins/bin/") || strings.HasPrefix(name, "plugins/ui/")
}
func (m Manifest) Validate() error {
	if m.Format != 1 || !versionPattern.MatchString(m.Version) || !commitPattern.MatchString(m.Commit) {
		return fmt.Errorf("invalid release identity or format")
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		return fmt.Errorf("release platform is incompatible with this program")
	}
	if m.DataSchema != buildinfo.DataSchema {
		return fmt.Errorf("release data schema %d is not supported by this program", m.DataSchema)
	}
	if len(m.Files) < 2 || len(m.Files) > MaxFiles {
		return fmt.Errorf("release file count is out of bounds")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range m.Files {
		hash, err := hex.DecodeString(f.SHA256)
		if !SafePath(f.Path) || !allowedFile(f.Path) || seen[f.Path] || err != nil || len(hash) != sha256.Size || f.Bytes < 0 || f.Bytes > MaxFileBytes {
			return fmt.Errorf("invalid release file declaration")
		}
		if (f.Path == "bin/luna" || strings.HasPrefix(f.Path, "plugins/bin/")) != f.Executable {
			return fmt.Errorf("invalid release executable mode")
		}
		total += f.Bytes
		if total > MaxTotalBytes {
			return fmt.Errorf("release exceeds total byte limit")
		}
		seen[f.Path] = true
	}
	if !seen["bin/luna"] || !seen["web/index.html"] {
		return fmt.Errorf("release is missing required runtime resources")
	}
	return nil
}
func Decode(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > MaxManifestBytes {
		return m, fmt.Errorf("release manifest is too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid release manifest")
	}
	if d.Decode(new(any)) != io.EOF {
		return m, fmt.Errorf("invalid trailing release data")
	}
	return m, m.Validate()
}
func Load(dir string) (Manifest, error) {
	var m Manifest
	root, err := os.OpenRoot(dir)
	if err != nil {
		return m, err
	}
	defer root.Close()
	f, err := openRegular(root, ManifestName)
	if err != nil {
		return m, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil {
		return m, err
	}
	return Decode(raw)
}
func openRegular(root *os.Root, name string) (*os.File, error) {
	if !SafePath(name) {
		return nil, fmt.Errorf("invalid release path")
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("release links are not allowed")
		}
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("release entry is not a regular file")
	}
	return f, nil
}
func checkFile(ctx context.Context, root *os.Root, file File, dst io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := openRegular(root, file.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != file.Bytes || (info.Mode().Perm()&0111 != 0) != file.Executable {
		return fmt.Errorf("release file size or mode mismatch: %s", file.Path)
	}
	hash := sha256.New()
	if dst == nil {
		dst = io.Discard
	}
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(f, file.Bytes+1))
	if err != nil {
		return err
	}
	if n != file.Bytes || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		return fmt.Errorf("release integrity mismatch: %s", file.Path)
	}
	return ctx.Err()
}
func Verify(ctx context.Context, dir string) (Manifest, error) {
	m, err := Load(dir)
	if err != nil {
		return m, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return m, err
	}
	defer root.Close()
	for _, f := range m.Files {
		if err = checkFile(ctx, root, f, nil); err != nil {
			return m, err
		}
	}
	return m, nil
}
func (m Manifest) File(name string) (File, bool) {
	for _, f := range m.Files {
		if f.Path == name {
			return f, true
		}
	}
	return File{}, false
}
func (m Manifest) Identity() string {
	raw, _ := json.Marshal(m)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func absoluteDestination(dest string) error {
	if !filepath.IsAbs(dest) || filepath.Clean(dest) == string(filepath.Separator) {
		return fmt.Errorf("destination must be a new absolute directory")
	}
	return nil
}
