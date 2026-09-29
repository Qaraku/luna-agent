package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func Pack(ctx context.Context, dir string, out io.Writer) error {
	m, err := Verify(ctx, dir)
	if err != nil {
		return err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(raw) > MaxManifestBytes {
		return fmt.Errorf("normalized release manifest is too large")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	defer gz.Close()
	defer tw.Close()
	if err = tw.WriteHeader(&tar.Header{Name: ManifestName, Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err = tw.Write(raw); err != nil {
		return err
	}
	for _, f := range m.Files {
		mode := int64(0600)
		if f.Executable {
			mode = 0700
		}
		if err = tw.WriteHeader(&tar.Header{Name: f.Path, Mode: mode, Size: f.Bytes, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if err = checkFile(ctx, root, f, tw); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Install只创建新目录；失败清理本次创建的候选，不替换已有文件或运行实例。
func Install(ctx context.Context, in io.Reader, dest string) (m Manifest, err error) {
	if err = absoluteDestination(dest); err != nil {
		return m, err
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		return m, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dest)
		}
	}()
	// 限制压缩流及展开流；多余成员或tar尾部不能绕过字节预算。
	gz, err := gzip.NewReader(io.LimitReader(in, MaxTotalBytes+(8<<20)))
	if err != nil {
		return m, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: MaxTotalBytes + (8 << 20)}
	tr := tar.NewReader(limited)
	header, err := tr.Next()
	if err != nil {
		return m, err
	}
	if header.Name != ManifestName || header.Typeflag != tar.TypeReg || header.Size > MaxManifestBytes || header.Size < 0 {
		return m, fmt.Errorf("archive must start with release manifest")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return m, err
	}
	m, err = Decode(raw)
	if err != nil {
		return m, err
	}
	declared := map[string]File{}
	for _, f := range m.Files {
		declared[f.Path] = f
	}
	for {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		header, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		file, ok := declared[header.Name]
		if !ok || header.Typeflag != tar.TypeReg || header.Size != file.Bytes {
			return m, fmt.Errorf("undeclared, duplicate or invalid archive file")
		}
		if header.Mode&^int64(0777) != 0 || (header.Mode&0111 != 0) != file.Executable {
			return m, fmt.Errorf("archive mode mismatch")
		}
		target := filepath.Join(dest, filepath.FromSlash(file.Path))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return m, err
		}
		mode := os.FileMode(0600)
		if file.Executable {
			mode = 0700
		}
		f, openErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if openErr != nil {
			return m, openErr
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, hash), tr)
		if copyErr == nil {
			copyErr = f.Sync()
		}
		closeErr := f.Close()
		if copyErr != nil {
			return m, copyErr
		}
		if closeErr != nil {
			return m, closeErr
		}
		if n != file.Bytes || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return m, fmt.Errorf("archive content integrity mismatch")
		}
		delete(declared, file.Path)
	}
	if len(declared) != 0 {
		return m, fmt.Errorf("archive is missing declared files")
	}
	// 读取gzip尾部，不能将CRC缺失/错误当作成功；有效tar后不接受第二份载荷。
	tail, err := io.Copy(io.Discard, limited)
	if err != nil {
		return m, err
	}
	if tail != 0 || limited.N <= 0 {
		return m, fmt.Errorf("archive has trailing data or exceeds byte limit")
	}
	if err = ctx.Err(); err != nil {
		return m, err
	}
	if err = os.WriteFile(filepath.Join(dest, ManifestName), raw, 0600); err != nil {
		return m, err
	}
	success = true
	return m, nil
}
