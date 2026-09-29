// Package privatebackup 提供显式本地私人快照，不执行被备份的代码，不打印文件内容。
package privatebackup

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
	"github.com/Qaraku/luna-agent/internal/release"
)

const ManifestName = "luna-private-backup.json"
const RestoreInfoName = "luna-restored.json"
const MaxManifestBytes = 4 << 20
const MaxFileBytes = int64(512 << 20)
const MaxTotalBytes = int64(4) << 30
const MaxEntries = 16384

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type Source struct {
	ID        string
	Path      string
	Target    string
	Directory bool
}
type SnapshotSource struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Directory bool   `json:"directory"`
	Present   bool   `json:"present"`
}
type Entry struct {
	Path       string `json:"path"`
	Directory  bool   `json:"directory,omitempty"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256,omitempty"`
	Executable bool   `json:"executable,omitempty"`
}
type Manifest struct {
	Format      int              `json:"format"`
	DataSchema  int              `json:"data_schema"`
	CreatedAt   time.Time        `json:"created_at"`
	Build       buildinfo.Info   `json:"build"`
	Sources     []SnapshotSource `json:"sources"`
	Entries     []Entry          `json:"entries"`
	ExtraSkills []string         `json:"extra_skills,omitempty"`
}

func isWithin(child, parent string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}
func validateTargets(sources []SnapshotSource) error {
	if len(sources) == 0 || len(sources) > 128 {
		return errors.New("backup source count is out of bounds")
	}
	ids := map[string]bool{}
	for i, s := range sources {
		if !idPattern.MatchString(s.ID) || ids[s.ID] || !release.SafePath(s.Target) || isWithin(s.Target, ManifestName) || isWithin(s.Target, RestoreInfoName) {
			return errors.New("invalid backup source identity or target")
		}
		ids[s.ID] = true
		for _, other := range sources[:i] {
			if isWithin(s.Target, other.Target) || isWithin(other.Target, s.Target) {
				return errors.New("backup targets overlap")
			}
		}
	}
	return nil
}
func (m Manifest) Validate() error {
	if m.Format != 1 || m.DataSchema != buildinfo.DataSchema {
		return errors.New("backup format or data schema is incompatible with this program")
	}
	if m.CreatedAt.IsZero() || len(m.Entries) > MaxEntries || len(m.ExtraSkills) > 32 {
		return errors.New("invalid backup metadata or entry count")
	}
	if err := validateTargets(m.Sources); err != nil {
		return err
	}
	seen := map[string]Entry{}
	var total int64
	for _, e := range m.Entries {
		if !release.SafePath(e.Path) || e.Bytes < 0 || e.Bytes > MaxFileBytes {
			return errors.New("invalid backup entry")
		}
		if _, exists := seen[e.Path]; exists {
			return errors.New("duplicate backup entry")
		}
		owner := false
		for _, s := range m.Sources {
			if s.Present && (e.Path == s.Target || s.Directory && isWithin(e.Path, s.Target)) {
				if !s.Directory && e.Directory {
					return errors.New("file source contains a directory")
				}
				owner = true
				break
			}
		}
		if !owner {
			return errors.New("entry is outside declared backup sources")
		}
		if e.Directory {
			if e.Bytes != 0 || e.SHA256 != "" || e.Executable {
				return errors.New("invalid backup directory")
			}
		} else {
			hash, err := hex.DecodeString(e.SHA256)
			if err != nil || len(hash) != 32 {
				return errors.New("invalid backup digest")
			}
		}
		total += e.Bytes
		if total > MaxTotalBytes {
			return errors.New("backup exceeds total byte limit")
		}
		seen[e.Path] = e
	}
	for _, e := range m.Entries {
		for parent := path.Dir(e.Path); parent != "."; parent = path.Dir(parent) {
			if entry, ok := seen[parent]; ok && !entry.Directory {
				return errors.New("backup file used as a parent directory")
			}
		}
	}
	for _, s := range m.Sources {
		entry, present := seen[s.Target]
		if s.Present && (!present || entry.Directory != s.Directory) {
			return errors.New("backup source root is missing")
		}
	}
	seenSkills := map[string]bool{}
	for _, dir := range m.ExtraSkills {
		entry, ok := seen[dir]
		if !release.SafePath(dir) || !strings.HasPrefix(dir, "imported-skills/") || seenSkills[dir] || !ok || !entry.Directory {
			return errors.New("invalid restored skill root")
		}
		seenSkills[dir] = true
	}
	return nil
}
func decode(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > MaxManifestBytes {
		return m, errors.New("backup manifest exceeds byte limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		return m, errors.New("invalid backup manifest")
	}
	return m, m.Validate()
}
func absoluteNew(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return fmt.Errorf("backup output must be a new absolute path")
	}
	return nil
}
