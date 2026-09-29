package privatebackup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

// Plan仅描述启动时确定的数据位置，不包含凭据正文或项目内容。
type Plan struct {
	Format      int      `json:"format"`
	ConfigDir   string   `json:"config_dir"`
	ConfigFile  string   `json:"config_file"`
	DataDir     string   `json:"data_dir"`
	StateDir    string   `json:"state_dir"`
	SessionsDir string   `json:"sessions_dir"`
	SkillDirs   []string `json:"skill_dirs,omitempty"`
}

func (p Plan) Validate() error {
	if p.Format != 1 || len(p.SkillDirs) > 32 {
		return errors.New("unsupported data plan format or skill root count")
	}
	paths := append([]string{p.ConfigDir, p.ConfigFile, p.DataDir, p.StateDir, p.SessionsDir}, p.SkillDirs...)
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) || len(path) > 4096 || strings.ContainsRune(path, 0) {
			return errors.New("data plan paths must be explicit absolute non-root paths")
		}
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 32<<10 {
		return errors.New("data plan exceeds byte limit")
	}
	return nil
}
func (p Plan) LockPaths() []string {
	// 额外技能与外部config只读，可能位于不可写的系统目录，不在旁边写锁。
	// 它们的外部编辑者不属于Luna协作进程，仍需停机确认且复制时校验变化。
	return []string{p.ConfigDir, p.DataDir, p.StateDir, p.SessionsDir}
}
func DecodePlan(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > 32<<10 {
		return p, errors.New("data plan exceeds byte limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, errors.New("invalid data plan")
	}
	return p, p.Validate()
}
