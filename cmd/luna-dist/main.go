// luna-dist从固定Git提交生成离线程序包，不操作tag或发布服务。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/plugins/jsonformat"
	"github.com/Qaraku/luna-agent/internal/release"
	"github.com/Qaraku/luna-agent/internal/sourcecopy"
)

var sourcePaths = []string{"go.mod", "go.sum", "cmd/luna", "internal", "plugins/text_transform", "plugins/read_file", "plugins/list_dir", "plugins/search_files", "plugins/find_files", "plugins/json_format", "plugins/ui", "web", "LICENSE"}

func exportSource(ctx context.Context, repository, commit, dest string) error {
	return sourcecopy.Export(ctx, repository, commit, dest, sourcePaths)
}
func buildEnv() []string {
	out := []string{}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOROOT"} {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return append(out, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
}
func build(ctx context.Context, source, dest, packagePath, version, commit string) error {
	args := []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", "-X github.com/Qaraku/luna-agent/internal/buildinfo.Version=" + version + " -X github.com/Qaraku/luna-agent/internal/buildinfo.Commit=" + commit, "-o", dest, packagePath}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = source
	cmd.Env = buildEnv()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s failed (dependencies must already be cached): %w", packagePath, err)
	}
	return nil
}
func copyResources(source, dest, name string) error {
	return filepath.WalkDir(filepath.Join(source, name), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("runtime resource must be a regular file")
		}
		// 不分发开发测试；资源来自提交，不从工作树取私人或未跟踪文件。
		if strings.HasSuffix(path, ".test.cjs") || strings.HasSuffix(path, ".test.mjs") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
}
func create(ctx context.Context, repository, commit, version, out string) error {
	if !regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`).MatchString(commit) {
		return errors.New("-commit must be a full local Git commit")
	}
	if version == "" {
		version = "dev-" + commit[:12]
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,95}$`).MatchString(version) {
		return errors.New("invalid version identity")
	}
	if !filepath.IsAbs(out) {
		return errors.New("-out must be an absolute new archive path")
	}
	tmp, err := os.MkdirTemp("", "luna-distribution-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	source, stage := filepath.Join(tmp, "source"), filepath.Join(tmp, "release")
	if err = os.MkdirAll(source, 0700); err != nil {
		return err
	}
	if err = exportSource(ctx, repository, commit, source); err != nil {
		return err
	}
	for _, dir := range []string{"bin", "plugins/bin"} {
		if err = os.MkdirAll(filepath.Join(stage, dir), 0700); err != nil {
			return err
		}
	}
	if err = build(ctx, source, filepath.Join(stage, "bin/luna"), "./cmd/luna", version, commit); err != nil {
		return err
	}
	tools := append(append([]pluginhost.ToolSpec{}, pluginhost.Allowlist...), jsonformat.Source())
	for _, tool := range tools {
		if err = build(ctx, source, filepath.Join(stage, "plugins/bin", tool.Dir), "./plugins/"+tool.Dir, version, commit); err != nil {
			return err
		}
	}
	for _, name := range []string{"web", "plugins/ui", "LICENSE"} {
		if err = copyResources(source, stage, name); err != nil {
			return err
		}
	}
	m := release.Manifest{Format: 1, Version: version, Commit: commit, OS: runtime.GOOS, Arch: runtime.GOARCH, DataSchema: buildinfo.DataSchema}
	err = filepath.WalkDir(stage, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		m.Files = append(m.Files, release.File{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(hash[:]), Bytes: int64(len(data)), Executable: info.Mode().Perm()&0111 != 0})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if err = m.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(stage, release.ManifestName), raw, 0600); err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(out)
		}
	}()
	if err = release.Pack(ctx, stage, f); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
func run(args []string) error {
	flags := flag.NewFlagSet("luna-dist", flag.ContinueOnError)
	repository := flags.String("repo", ".", "local Luna repository")
	commit := flags.String("commit", "", "full local commit, not a branch/tag")
	version := flags.String("version", "", "build identity (default dev-<commit>); creates no tag")
	out := flags.String("out", "", "new absolute tar.gz path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected argument")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return create(ctx, *repository, *commit, *version, *out)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "luna-dist:", err)
		os.Exit(1)
	}
}
