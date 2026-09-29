// Package sourcecopy 从固定本地Git对象复制源码；不检出、不读取工作树文件或私人目录。
package sourcecopy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Qaraku/luna-agent/internal/release"
)

var CommitPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("Git object output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, repo string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	for _, key := range []string{"PATH", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out := &cappedBuffer{limit: limit}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cannot read selected local Git object: %w", err)
	}
	return out.Bytes(), nil
}
func Export(ctx context.Context, repo, commit, dest string, paths []string) error {
	return ExportFiltered(ctx, repo, commit, dest, paths, nil)
}

// ExportFiltered在读取blob正文之前检查文件名；拒绝不合适的已提交路径而非静默遗漏。
func ExportFiltered(ctx context.Context, repo, commit, dest string, paths []string, allow func(string) bool) error {
	if !CommitPattern.MatchString(commit) || len(paths) == 0 {
		return errors.New("source export requires a complete commit and explicit paths")
	}
	for _, p := range paths {
		if !release.SafePath(p) {
			return errors.New("invalid source selection")
		}
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("source destination must be empty")
	}
	kind, err := command(ctx, repo, 64, "cat-file", "-t", commit)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(kind)) != "commit" {
		return errors.New("source identity is not a commit")
	}
	raw, err := command(ctx, repo, 4<<20, append([]string{"ls-tree", "-r", "-z", commit, "--"}, paths...)...)
	if err != nil {
		return err
	}
	count := 0
	var total int64
	for _, row := range bytes.Split(raw, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(row), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") || !release.SafePath(name) {
			return errors.New("unsupported tracked source entry (links and submodules are not copied)")
		}
		if allow != nil && !allow(name) {
			return errors.New("selected commit contains an excluded source path")
		}
		count++
		if count > 10000 {
			return errors.New("source file limit exceeded")
		}
		data, err := command(ctx, repo, release.MaxFileBytes, "cat-file", "blob", fields[2])
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > release.MaxTotalBytes {
			return errors.New("source byte limit exceeded")
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if fields[0] == "100755" {
			mode = 0700
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if count == 0 {
		return errors.New("selected commit contains no requested sources")
	}
	return ctx.Err()
}
