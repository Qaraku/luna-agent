package privatebackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Qaraku/luna-agent/internal/datalifecycle"
)

func Inspect(ctx context.Context, archive string) (Manifest, error) { return consume(ctx, archive, "") }
func Restore(ctx context.Context, archive, dest string) (m Manifest, err error) {
	if err = absoluteNew(dest); err != nil {
		return m, err
	}
	if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return m, statErr
		}
		return m, errors.New("restore destination already exists")
	}
	guard, lockErr := datalifecycle.Reserve([]string{dest})
	if lockErr != nil {
		return m, lockErr
	}
	defer guard.Close()
	if err = os.Mkdir(dest, 0700); err != nil {
		return m, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dest)
		}
	}()
	pending := filepath.Join(dest, datalifecycle.PendingRestoreName)
	pendingFile, createErr := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if createErr != nil {
		return m, createErr
	}
	_, writeErr := pendingFile.WriteString("Restore is incomplete; do not use this directory.\n")
	if writeErr == nil {
		writeErr = pendingFile.Sync()
	}
	closeErr := pendingFile.Close()
	if writeErr != nil {
		return m, writeErr
	}
	if closeErr != nil {
		return m, closeErr
	}
	m, err = consume(ctx, archive, dest)
	if err != nil {
		return m, err
	}
	// 只写来源版本与额外技能根的说明，不保存原机绝对路径或复制执行授权。
	raw, err := json.Marshal(struct {
		Format      int      `json:"format"`
		BackupBuild any      `json:"backup_build"`
		DataSchema  int      `json:"data_schema"`
		ExtraSkills []string `json:"extra_skills,omitempty"`
	}{1, m.Build, m.DataSchema, m.ExtraSkills})
	if err != nil {
		return m, err
	}
	if err = os.WriteFile(filepath.Join(dest, RestoreInfoName), raw, 0600); err != nil {
		return m, err
	}
	if err = os.Remove(pending); err != nil {
		return m, err
	}
	if err = guard.Initialize(); err != nil {
		return m, err
	}
	ok = true
	return m, nil
}
func consume(ctx context.Context, archive, dest string) (m Manifest, err error) {
	before, statErr := os.Lstat(archive)
	if statErr != nil {
		return m, statErr
	}
	if !before.Mode().IsRegular() {
		return m, errors.New("private archive must be a regular file, not a link or special device")
	}
	file, err := os.Open(archive)
	if err != nil {
		return m, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxTotalBytes+(16<<20) {
		return m, errors.New("private archive is not a bounded regular file")
	}
	gz, err := gzip.NewReader(io.LimitReader(contextReader{ctx, file}, MaxTotalBytes+(16<<20)))
	if err != nil {
		return m, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: MaxTotalBytes + (16 << 20)}
	tr := tar.NewReader(limited)
	h, err := tr.Next()
	if err != nil {
		return m, err
	}
	if h.Name != ManifestName || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > MaxManifestBytes {
		return m, errors.New("private archive must begin with its manifest")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return m, err
	}
	m, err = decode(raw)
	if err != nil {
		return m, err
	}
	remaining := map[string]Entry{}
	for _, e := range m.Entries {
		remaining[e.Path] = e
	}
	for {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		h, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		entry, ok := remaining[h.Name]
		kind := byte(tar.TypeReg)
		if entry.Directory {
			kind = tar.TypeDir
		}
		if !ok || h.Typeflag != kind || h.Size != entry.Bytes || h.Mode&^int64(0777) != 0 {
			return m, errors.New("undeclared, duplicate or invalid private archive entry")
		}
		if !entry.Directory && (h.Mode&0111 != 0) != entry.Executable {
			return m, errors.New("private archive mode mismatch")
		}
		if entry.Directory {
			if dest != "" {
				if err = os.MkdirAll(filepath.Join(dest, filepath.FromSlash(entry.Path)), 0700); err != nil {
					return m, err
				}
			}
		} else {
			var output *os.File
			var sink io.Writer = io.Discard
			if dest != "" {
				target := filepath.Join(dest, filepath.FromSlash(entry.Path))
				if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					return m, err
				}
				mode := os.FileMode(0600)
				if entry.Executable {
					mode = 0700
				}
				output, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
				if err != nil {
					return m, err
				}
				sink = output
			}
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(sink, hash), contextReader{ctx, tr})
			if output != nil {
				if copyErr == nil {
					copyErr = output.Sync()
				}
				closeErr := output.Close()
				if copyErr == nil {
					copyErr = closeErr
				}
			}
			if copyErr != nil {
				return m, copyErr
			}
			if n != entry.Bytes || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
				return m, errors.New("private archive content digest mismatch")
			}
		}
		delete(remaining, entry.Path)
	}
	if len(remaining) != 0 {
		return m, errors.New("private archive is missing declared entries")
	}
	tail, err := io.Copy(io.Discard, limited)
	if err != nil {
		return m, err
	}
	if tail != 0 || limited.N <= 0 {
		return m, errors.New("private archive has extra payload or exceeds byte limit")
	}
	return m, ctx.Err()
}
