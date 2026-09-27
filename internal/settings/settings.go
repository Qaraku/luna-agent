// Package settings 管理 Luna 自己写的用户偏好文件。
//
// 它与 `internal/config` 是同一目录下的另一个文件，读者不同：`config.yaml` 由用户
// 手写、Luna 只读（用哪个模型、哪个 provider）；`settings.yaml` 由 Luna 写，记录用户
// 在产品里做出的选择，目前只有一个——哪些 skill 被停用。分开的理由就是把一个会被程序
// 重写的开关放进用户手写的文件里，等于为了改一个开关而重写用户的注释与排版。
//
// 两个文件都拒绝未知键，区别只在"谁写它"。正因为这个文件由 Luna 自己重写，一个手写
// 错的字段被静默丢掉比报错更难查：用户看到的是自己的设置不生效，而文件看起来是对的。
// 所以这里同样打开 `KnownFields(true)`——即使写入方是我们自己，读入方也要对文件内容
// 负责。
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

	yaml "gopkg.in/yaml.v3"
)

// FileName is the file Luna reads and writes inside the user's configuration
// directory. It is a name, not a path: where the directory is belongs to
// internal/layout.
const FileName = "settings.yaml"

// The permissions Luna gives the file it creates and the directory it may have
// to create. The directory is the same place config.yaml lives, so it is
// already 0700 on most machines; when Luna does create it, it creates it
// private rather than world-readable, because a preference file is nobody
// else's business.
const (
	fileMode fs.FileMode = 0o600
	dirMode  fs.FileMode = 0o700
)

// Settings is everything Luna keeps about the choices a user made in the
// product, as opposed to the configuration they wrote by hand.
type Settings struct {
	Skills Skills `yaml:"skills"`
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
// The rewrite goes through a temporary file in the same directory and a rename:
// a preference file is small, so a whole rewrite costs nothing, and it cannot
// leave the half-written state an in-place edit could. The rename is what makes
// that true — until it happens the old contents are what any reader sees.
//
// A failure removes the temporary file, so a save that did not happen leaves
// the directory exactly as it was. The settings are normalized on the way out:
// see Settings.normalized.
func Save(path string, file Settings) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(dir), reason(err))
	}
	data, err := marshal(file)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), reason(err))
	}
	tempName := temp.Name()
	// CreateTemp already opens 0600; setting it explicitly keeps the intent
	// here instead of in the standard library's documentation.
	if err := temp.Chmod(fileMode); err != nil {
		temp.Close()
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", filepath.Base(path), reason(err))
	}
	_, writeErr := temp.Write(data)
	closeErr := temp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", filepath.Base(path), reason(writeErr))
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", filepath.Base(path), reason(err))
	}
	return nil
}

// DisabledSkills returns the names of the skills the user turned off, without
// blanks or repeats. Absence means on, so an empty result is the normal state of
// a user who has not turned anything off.
func (f Settings) DisabledSkills() []string { return f.normalized().Skills.Disabled }

// WithSkillDisabled returns these settings with one skill turned off or on.
// Every other entry keeps its place, so turning one skill off never moves
// another, and turning the same skill off twice is written once.
func (f Settings) WithSkillDisabled(name string, disabled bool) Settings {
	target := strings.TrimSpace(name)
	current := f.normalized()
	if target == "" {
		return current
	}
	kept := make([]string, 0, len(current.Skills.Disabled)+1)
	for _, existing := range current.Skills.Disabled {
		if existing != target {
			kept = append(kept, existing)
		}
	}
	if disabled {
		kept = append(kept, target)
	}
	return Settings{Skills: Skills{Disabled: kept}}
}

// normalized returns the file with the disabled list in its canonical form:
// blanks dropped, repeats dropped, first-seen order kept. This list is the one
// thing in the file that grows by a user action at a time, so without this a
// name turned off, on and off again would be written twice.
func (f Settings) normalized() Settings {
	seen := make(map[string]bool, len(f.Skills.Disabled))
	disabled := make([]string, 0, len(f.Skills.Disabled))
	for _, name := range f.Skills.Disabled {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		disabled = append(disabled, trimmed)
	}
	return Settings{Skills: Skills{Disabled: disabled}}
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
