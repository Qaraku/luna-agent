package privatebackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
	"github.com/Qaraku/luna-agent/internal/datalifecycle"
)

type captured struct {
	root  *os.Root
	name  string
	entry Entry
}
type contextReader struct {
	context context.Context
	reader  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func fileDigest(ctx context.Context, root *os.Root, name string, expected *Entry, dst io.Writer) (Entry, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return Entry{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return Entry{}, errors.New("backup entry is not a bounded regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	entry := Entry{Bytes: info.Size(), Executable: info.Mode().Perm()&0111 != 0}
	if expected != nil && (expected.Bytes != entry.Bytes || expected.Executable != entry.Executable) {
		return entry, errors.New("private file changed during backup")
	}
	hash := sha256.New()
	if dst == nil {
		dst = io.Discard
	}
	n, err := io.Copy(io.MultiWriter(hash, dst), io.LimitReader(contextReader{ctx, f}, entry.Bytes+1))
	if err != nil {
		return entry, err
	}
	entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if n != entry.Bytes || expected != nil && entry.SHA256 != expected.SHA256 {
		return entry, errors.New("private file changed during backup")
	}
	return entry, nil
}

// Backup的调用方必须在来源解析之前持有对应数据空间的生命周期锁。
func Backup(ctx context.Context, sources []Source, out string, extraSkills []string) (m Manifest, err error) {
	if err = ctx.Err(); err != nil {
		return m, err
	}
	if err = absoluteNew(out); err != nil {
		return m, err
	}
	m = Manifest{Format: 1, DataSchema: buildinfo.DataSchema, CreatedAt: time.Now().UTC(), Build: buildinfo.Current(), Entries: []Entry{}}
	for _, s := range sources {
		if !filepath.IsAbs(s.Path) {
			return m, errors.New("backup source must be absolute")
		}
		m.Sources = append(m.Sources, SnapshotSource{ID: s.ID, Target: s.Target, Directory: s.Directory})
	}
	if err = validateTargets(m.Sources); err != nil {
		return m, err
	}
	canonicalOut, err := datalifecycle.Canonical(out)
	if err != nil {
		return m, err
	}
	capturedFiles := []captured{}
	roots := []*os.Root{}
	defer func() {
		for _, r := range roots {
			_ = r.Close()
		}
	}()
	var total int64
	appendEntry := func(e Entry) error {
		total += e.Bytes
		if total > MaxTotalBytes || len(m.Entries) >= MaxEntries {
			return errors.New("private backup exceeds byte or entry limit")
		}
		m.Entries = append(m.Entries, e)
		return nil
	}
	present := 0
	for index, source := range sources {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		canonical, err := datalifecycle.Canonical(source.Path)
		if err != nil {
			return m, err
		}
		relative, relErr := filepath.Rel(canonical, canonicalOut)
		if canonical == canonicalOut || source.Directory && relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return m, errors.New("backup output must be outside every source")
		}
		info, err := os.Lstat(canonical)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return m, err
		}
		if info.IsDir() != source.Directory || !info.IsDir() && !info.Mode().IsRegular() {
			return m, fmt.Errorf("backup source %s has the wrong file type", source.ID)
		}
		m.Sources[index].Present = true
		present++
		if !source.Directory {
			opened, err := os.OpenRoot(filepath.Dir(canonical))
			if err != nil {
				return m, err
			}
			roots = append(roots, opened)
			entry, err := fileDigest(ctx, opened, filepath.Base(canonical), nil, nil)
			if err != nil {
				return m, fmt.Errorf("snapshot %s: %w", source.ID, err)
			}
			entry.Path = source.Target
			if err = appendEntry(entry); err != nil {
				return m, err
			}
			capturedFiles = append(capturedFiles, captured{opened, filepath.Base(canonical), entry})
			continue
		}
		opened, err := os.OpenRoot(canonical)
		if err != nil {
			return m, err
		}
		roots = append(roots, opened)
		err = fs.WalkDir(opened.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(name) > 4096 {
				return errors.New("backup path exceeds limit")
			}
			// 生命周期旁置文件不是私人资料；嵌套配置根可能使它们落在另一个来源内。
			if d.Name() == ".luna-data-format.json" || strings.HasPrefix(d.Name(), ".luna-data-") && (strings.HasSuffix(d.Name(), ".lock") || strings.HasSuffix(d.Name(), ".format.json")) {
				if d.IsDir() {
					return errors.New("data marker must not be a directory")
				}
				return nil
			}
			target := source.Target
			if name != "." {
				target += "/" + name
			}
			if d.IsDir() {
				return appendEntry(Entry{Path: target, Directory: true})
			}
			if !d.Type().IsRegular() {
				return errors.New("private backup refuses links and special files")
			}
			entry, err := fileDigest(ctx, opened, name, nil, nil)
			if err != nil {
				return err
			}
			entry.Path = target
			if err = appendEntry(entry); err != nil {
				return err
			}
			capturedFiles = append(capturedFiles, captured{opened, name, entry})
			return nil
		})
		if err != nil {
			return m, fmt.Errorf("snapshot %s: %w", source.ID, err)
		}
	}
	if present == 0 {
		return m, errors.New("none of the planned private data sources exist")
	}
	for _, dir := range extraSkills {
		for _, s := range m.Sources {
			if s.Present && s.Directory && s.Target == dir {
				m.ExtraSkills = append(m.ExtraSkills, dir)
			}
		}
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	if err = m.Validate(); err != nil {
		return m, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	if len(raw) > MaxManifestBytes {
		return m, errors.New("private manifest exceeds byte limit")
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return m, err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(out)
		}
	}()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	defer gz.Close()
	defer tw.Close()
	if err = tw.WriteHeader(&tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(raw))}); err != nil {
		return m, err
	}
	if _, err = tw.Write(raw); err != nil {
		return m, err
	}
	byPath := map[string]captured{}
	for _, f := range capturedFiles {
		byPath[f.entry.Path] = f
	}
	for _, entry := range m.Entries {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		mode := int64(0600)
		kind := byte(tar.TypeReg)
		if entry.Directory {
			mode = 0700
			kind = tar.TypeDir
		} else if entry.Executable {
			mode = 0700
		}
		if err = tw.WriteHeader(&tar.Header{Name: entry.Path, Typeflag: kind, Mode: mode, Size: entry.Bytes}); err != nil {
			return m, err
		}
		if !entry.Directory {
			f := byPath[entry.Path]
			if _, err = fileDigest(ctx, f.root, f.name, &entry, tw); err != nil {
				return m, err
			}
		}
	}
	if err = tw.Close(); err != nil {
		return m, err
	}
	if err = gz.Close(); err != nil {
		return m, err
	}
	if err = file.Sync(); err != nil {
		return m, err
	}
	if err = file.Close(); err != nil {
		return m, err
	}
	ok = true
	return m, nil
}
