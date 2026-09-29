package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Qaraku/luna-agent/internal/fileread"
)

// PrebuiltBinary只能由可信装配提供；浏览器重载仍只选择工具身份。
type PrebuiltBinary struct {
	Path   string
	SHA256 string
}

func (b PrebuiltBinary) validate() error {
	hash, err := hex.DecodeString(b.SHA256)
	if err != nil || len(hash) != sha256.Size || !filepath.IsAbs(b.Path) {
		return fmt.Errorf("invalid prebuilt tool")
	}
	return nil
}

func (h *Host) prepareExecutable(parent context.Context, spec ToolSpec) (string, error) {
	ctx, cancel := context.WithTimeout(parent, h.opts.BuildTimeout)
	defer cancel()
	runtimeDir := h.opts.RuntimeDir
	if runtimeDir == "" {
		runtimeDir = filepath.Join(h.root, ".runtime")
	}
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(runtimeDir, "plugin-"+spec.Tool+"-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if h.opts.Prebuilt != nil {
		binary := h.opts.Prebuilt[spec.Tool]
		if err := binary.validate(); err != nil {
			return "", err
		}
		info, err := os.Lstat(binary.Path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > 128<<20 || info.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("invalid prebuilt binary file")
		}
		in, err := os.Open(binary.Path)
		if err != nil {
			return "", err
		}
		defer in.Close()
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(in, (128<<20)+1))
		if err != nil {
			return "", err
		}
		if n > 128<<20 || hex.EncodeToString(hash.Sum(nil)) != binary.SHA256 {
			return "", fmt.Errorf("prebuilt tool %s integrity mismatch", spec.Tool)
		}
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if err = f.Chmod(0700); err != nil {
			return "", err
		}
		if err = f.Close(); err != nil {
			return "", err
		}
	} else {
		_ = f.Close()
		source, err := fileread.ResolveDir(filepath.Join(h.root, "plugins"), spec.Dir)
		if err != nil {
			return "", fmt.Errorf("source for %s: %w", spec.Tool, err)
		}
		relative, err := filepath.Rel(h.root, source)
		if err != nil {
			return "", err
		}
		dir := "./" + filepath.ToSlash(relative)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", path, dir)
		cmd.Dir = h.root
		cmd.Env = minimalEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("build %s: %w: %.2000s", dir, err, out)
		}
	}
	ok = true
	return path, nil
}
