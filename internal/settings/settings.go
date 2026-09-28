// Package settings 管理 Luna 自己写的用户偏好文件。
//
// 它与 `internal/config` 是同一目录下的另一个文件，读者不同：`config.yaml` 由用户
// 手写、Luna 只读（用哪个模型、哪个 provider）；`settings.yaml` 由 Luna 写，记录用户
// 在产品里做出的选择：哪些 skill 被停用、用户显式打开过哪些默认关闭的能力，以及用户
// 显式允许 Luna 写入哪些目录。分开的理由就是把一个会被程序重写的开关放进用户手写的
// 文件里，等于为了改一个开关而重写用户的注释与排版。
//
// 两个文件都拒绝未知键，区别只在"谁写它"。正因为这个文件由 Luna 自己重写，一个手写
// 错的字段被静默丢掉比报错更难查：用户看到的是自己的设置不生效，而文件看起来是对的。
// 所以这里同样打开 `KnownFields(true)`——即使写入方是我们自己，读入方也要对文件内容
// 负责。
//
// 每个 section 都是具名类型，而不是一张 `map[string][]string` 式的权限表：一张 map 会
// 安静地接受一个拼错的种类，那时用户看到的不是"拼错了"，而是"我明明授权了它却还是不许
// 写"——一个看起来像 Luna 的 bug 的笔误。多一个 section 就多一次编译期检查。
//
// 写入是整文件重写，通过同目录临时文件加 `os.Rename` 完成。这个文件小到一次重写没有
// 代价，而就地改一个用户的偏好文件会留下半写状态：一个读不出来的偏好文件和一个不生效
// 的开关，用户无法区分。
package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Qaraku/luna-agent/internal/atomicfile"

	yaml "gopkg.in/yaml.v3"
)

// FileName is the file Luna reads and writes inside the user's configuration
// directory. It is a name, not a path: where the directory is belongs to
// internal/layout.
const FileName = "settings.yaml"

// The permissions Luna gives the file it creates. The directory it may have to
// create is internal/atomicfile's business, along with the temporary file and
// the rename: a preference file is nobody else's business, so it is private.
const fileMode fs.FileMode = 0o600

// Settings is everything Luna keeps about the choices a user made in the
// product, as opposed to the configuration they wrote by hand.
type Settings struct {
	// 三个 section 都带 `omitempty`：这个文件只说用户真的做过的事，没被停用的
	// skill、没有被显式打开的能力、没有被允许写入的目录都不写字。空文件因此是空
	// 配置，读回来也是空。
	Skills       Skills       `yaml:"skills,omitempty"`
	Capabilities Capabilities `yaml:"capabilities,omitempty"`
	Write        Write        `yaml:"write,omitempty"`
}

// Write is what the user lets Luna change on this machine: the directories the
// model's write tool may touch. Nothing else about a file is decided here — the
// boundary the read tools already keep (a path is resolved and checked against
// the session's directories) still applies, and this list can only narrow it.
//
// Absence means Luna writes nowhere, the same way absence in the other two
// sections means off. That default is the point: a capability that changes files
// on someone's machine is one they have to have asked for, by directory, and a
// blank or relative entry is not something they could have asked for.
type Write struct {
	// Dirs are the directories Luna may write inside, as absolute paths, in the
	// order the user allowed them.
	Dirs []string `yaml:"dirs,omitempty"`
}

// Capabilities is what the user decided about the capabilities that are off
// until they say otherwise.
type Capabilities struct {
	// Enabled names the capabilities the user explicitly turned on. These are
	// the exceptions in the other direction from Skills.Disabled: a capability
	// in this list is one the product does not run on its own, and absence
	// means off. Turning one on has to survive a restart, and this list is the
	// only place that says so — which is why the key is written only when there
	// is something to say.
	Enabled []string `yaml:"enabled,omitempty"`
}

// Skills is what the user decided about the skills installed on this machine.
type Skills struct {
	// Disabled names the skills the user turned off. Absence is the default and
	// means on: a skill directory is usable the moment it appears, and the file
	// carries only the exceptions. An empty list and a missing key mean the same
	// thing, which is why the key is written only when there is something to say.
	Disabled []string `yaml:"disabled,omitempty"`
}

// Load reads the settings file at path.
//
// A file that is not there is not an error: a runtime that refused to start
// because a preference file it may never need is missing would be inventing a
// problem. The second return value reports whether a file was found, the same
// shape internal/config uses, so a caller can say which source it used.
//
// Unknown keys are refused here for the same reason config.yaml refuses them,
// and one more: this file is written by Luna, so a key nobody recognises is
// either a hand edit that will not take effect or a field this version does not
// know — both are things the user needs to hear about rather than lose silently.
func Load(path string) (Settings, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Settings{}, false, nil
	}
	if err != nil {
		// The message names the file, not the directory it sits in: a settings
		// error is read by a person who knows where their own files are.
		return Settings{}, false, fmt.Errorf("read %s: %w", filepath.Base(path), reason(err))
	}
	var file Settings
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty file is empty settings, not a broken one.
			return Settings{}, true, nil
		}
		return Settings{}, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return file, true, nil
}

// Save writes the settings, replacing whatever was there.
//
// The rewrite goes through internal/atomicfile: a temporary file in the same
// directory, an explicit mode, then a rename. A preference file is small, so a
// whole rewrite costs nothing, and it cannot leave the half-written state an
// in-place edit could — until the rename the old contents are what any reader
// sees. The settings are normalized on the way out: see Settings.normalized.
func Save(path string, file Settings) error {
	data, err := marshal(file)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, fileMode)
}

// DisabledSkills returns the names of the skills the user turned off, without
// blanks or repeats. Absence means on, so an empty result is the normal state of
// a user who has not turned anything off.
func (f Settings) DisabledSkills() []string { return f.normalized().Skills.Disabled }

// EnabledCapabilities returns the names of the capabilities the user turned on,
// without blanks or repeats. Absence means off, so an empty result is the normal
// state of a user who has not enabled anything.
func (f Settings) EnabledCapabilities() []string { return f.normalized().Capabilities.Enabled }

// CapabilityEnabled reports whether the user turned this capability on. A
// capability the product does not run on its own is off unless its id is in the
// list, so a name that is not there is the ordinary state and not a missing
// entry — which is why this answers a question rather than returning the list.
func (f Settings) CapabilityEnabled(id string) bool {
	for _, name := range f.EnabledCapabilities() {
		if name == id {
			return true
		}
	}
	return false
}

// WithSkillDisabled returns these settings with one skill turned off or on.
// Every other entry keeps its place, so turning one skill off never moves
// another, and turning the same skill off twice is written once.
func (f Settings) WithSkillDisabled(name string, disabled bool) Settings {
	current := f.normalized()
	kept := withName(current.Skills.Disabled, name, disabled)
	current.Skills = Skills{Disabled: kept}
	return current
}

// WithCapabilityEnabled returns these settings with one capability turned on or
// off. It is the same shape as WithSkillDisabled, in the other direction: a
// capability is off unless its name is in the list. Every other entry keeps its
// place, and turning the same capability on twice is written once.
func (f Settings) WithCapabilityEnabled(id string, enabled bool) Settings {
	current := f.normalized()
	kept := withName(current.Capabilities.Enabled, id, enabled)
	current.Capabilities = Capabilities{Enabled: kept}
	return current
}

// WriteDirs returns the directories Luna may write inside, without blanks,
// repeats or entries that could not name a directory on this machine. Absence
// means nowhere, so an empty result is the normal state of a user who has
// allowed nothing.
func (f Settings) WriteDirs() []string { return f.normalized().Write.Dirs }

// WithWriteDir returns these settings with one directory allowed or no longer
// allowed. Every other entry keeps its place, so allowing one directory never
// moves another; the same directory allowed twice is written once.
//
// The entry is cleaned, not just trimmed: two spellings of one directory
// ("/home/j/luna/" and "/home/j/luna") are one grant, because the check this
// feeds compares against a resolved path. An entry that is not an absolute path
// cannot be a grant at all, so it is dropped rather than written and then
// silently never matched.
func (f Settings) WithWriteDir(dir string, allowed bool) Settings {
	current := f.normalized()
	current.Write = Write{Dirs: withPath(current.Write.Dirs, dir, allowed)}
	return current
}

// WithWriteDirs returns these settings with the whole list of allowed directories
// replaced.
//
// The settings page owns the list and therefore submits the list: a directory that is
// no longer in it is not allowed any more, which is what makes revoking one work
// without anyone having to track which change added it. Entries are normalized on the
// way out like every other list in this file.
func (f Settings) WithWriteDirs(dirs []string) Settings {
	current := f.normalized()
	current.Write = Write{Dirs: append([]string(nil), dirs...)}
	return current
}

// withName returns names with one name added or removed: trimming it, dropping
// it wherever it already was, and appending it at the end when add is true. The
// rest keep their order, so one change never moves another entry.
func withName(names []string, name string, add bool) []string {
	return withEntry(names, strings.TrimSpace(name), add)
}

// withPath is withName for directories, with one difference: the entry is an
// absolute cleaned path or it is not an entry.
func withPath(paths []string, path string, add bool) []string {
	return withEntry(paths, cleanPath(path), add)
}

// withEntry returns entries with one target added or removed: dropping it
// wherever it already was, and appending it at the end when add is true. The
// rest keep their order, so one change never moves another entry. An empty
// target is not an entry and leaves the list as it is.
func withEntry(entries []string, target string, add bool) []string {
	if target == "" {
		return append([]string(nil), entries...)
	}
	kept := make([]string, 0, len(entries)+1)
	for _, existing := range entries {
		if existing != target {
			kept = append(kept, existing)
		}
	}
	if add {
		kept = append(kept, target)
	}
	return kept
}

// cleanPath returns the cleaned absolute form of a directory, or "" when the
// path cannot name a directory on this machine.
func cleanPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if !filepath.IsAbs(trimmed) {
		return ""
	}
	return filepath.Clean(trimmed)
}

// normalized returns the file with all three lists in their canonical form:
// blanks dropped, repeats dropped, first-seen order kept. These lists are the
// ones that grow by a user action at a time, so without this a name turned off,
// on and off again would be written twice. Paths are cleaned as well as
// normalized; names are not, because a name is a label rather than a path that
// gets compared to one.
func (f Settings) normalized() Settings {
	return Settings{
		Skills:       Skills{Disabled: normalizeNames(f.Skills.Disabled)},
		Capabilities: Capabilities{Enabled: normalizeNames(f.Capabilities.Enabled)},
		Write:        Write{Dirs: normalizePaths(f.Write.Dirs)},
	}
}

// normalizeNames drops blanks and repeats while keeping first-seen order. Both
// name lists are sets of names the user toggled, so they share one rule rather
// than each carrying its own copy of it.
func normalizeNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	kept := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		kept = append(kept, trimmed)
	}
	return kept
}

// normalizePaths drops blanks, repeats and paths that could never name a
// directory on this machine, keeping first-seen order. A grant is compared
// against a resolved path, so only an absolute cleaned path can ever match one;
// a relative entry is not a grant the user made, and writing it down again would
// keep it in the file as if it were.
func normalizePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := cleanPath(path)
		if cleaned == "" || seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		kept = append(kept, cleaned)
	}
	return kept
}

// marshal renders the file the user will read. The indentation is the two
// spaces every YAML example in this project uses, and the encoding is of the
// normalized file, so two writes of the same settings produce the same bytes.
func marshal(file Settings) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(file.normalized()); err != nil {
		return nil, fmt.Errorf("encode settings: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode settings: %w", err)
	}
	return buffer.Bytes(), nil
}

// reason reduces a path error to its underlying reason, so an error about a
// settings file does not echo the host's directory layout.
func reason(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
