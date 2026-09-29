// Package datalifecycle 串行化使用同一私人数据空间的协作进程，拒绝未知格式。
package datalifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
)

const PendingRestoreName = ".luna-restore-pending"

type Guard struct {
	files   []*os.File
	formats []string
	mu      sync.Mutex
	closed  bool
	once    sync.Once
}

func (g *Guard) Close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.closed = true
		for i := len(g.files) - 1; i >= 0; i-- {
			_ = g.files[i].Close()
		}
	})
}

// Canonical支持尚未创建的数据根；只解析已有祖先，不通过创建目录改变旧布局选择。
func Canonical(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return "", errors.New("data root must be an absolute non-root path")
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		if resolved == string(filepath.Separator) {
			return "", errors.New("data root cannot resolve to the filesystem root")
		}
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("data root contains a dangling link: %w", err)
	}
	parent := filepath.Dir(path)
	if parent == string(filepath.Separator) {
		return path, nil
	}
	resolved, err = Canonical(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
func sidecars(path string) (string, string, error) {
	canonical, err := Canonical(path)
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256([]byte(canonical))
	stem := filepath.Join(filepath.Dir(canonical), ".luna-data-"+hex.EncodeToString(hash[:16]))
	return stem + ".lock", filepath.Join(canonical, ".luna-data-format.json"), nil
}

type marker struct {
	Format int `json:"format"`
	Schema int `json:"data_schema"`
}

func checkMarker(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return false, errors.New("invalid data format marker")
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return false, errors.New("invalid data format marker")
	}
	var value marker
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Format != 1 {
		return false, errors.New("invalid data format marker")
	}
	if value.Schema != buildinfo.DataSchema {
		return false, fmt.Errorf("data schema %d is incompatible with this program; use a compatible program or backup", value.Schema)
	}
	return true, nil
}
func Acquire(paths []string) (*Guard, error) {
	guard, err := Reserve(paths)
	if err != nil {
		return nil, err
	}
	if err = guard.Initialize(); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

// Reserve只占有锁；恢复使用它预留尚不存在的目标，避免提前创建目标目录。
func Reserve(paths []string) (guard *Guard, err error) {
	if len(paths) == 0 {
		return nil, errors.New("data guard requires explicit roots")
	}
	names := map[string]string{}
	for _, path := range paths {
		lock, format, err := sidecars(path)
		if err != nil {
			return nil, err
		}
		names[lock] = format
	}
	keys := make([]string, 0, len(names))
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	guard = &Guard{}
	acquired := guard
	ok := false
	defer func() {
		if !ok {
			acquired.Close()
		}
	}()
	for _, lock := range keys {
		if err = os.MkdirAll(filepath.Dir(lock), 0700); err != nil {
			return nil, err
		}
		file, err := openLocked(lock)
		if err != nil {
			return nil, fmt.Errorf("private data is in use or cannot be locked: %w", err)
		}
		guard.files = append(guard.files, file)
	}
	for _, lock := range keys {
		guard.formats = append(guard.formats, names[lock])
	}
	ok = true
	return guard, nil
}

// Initialize在持锁期间核对所有根，再为已选定的位置创建可随数据迁移的格式标记。
func (g *Guard) Initialize() (err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("data guard is closed")
	}
	missing := []string{}
	for _, path := range g.formats {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(path), PendingRestoreName)); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return errors.New("private data contains an incomplete restore; restore the backup into a new directory")
		}
		exists, err := checkMarker(path)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, path)
		}
	}
	raw, _ := json.Marshal(marker{Format: 1, Schema: buildinfo.DataSchema})
	for _, path := range missing {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(raw)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			_ = os.Remove(path)
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}

	return nil
}
