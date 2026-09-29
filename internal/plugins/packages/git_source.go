package packages

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var packageRevision = regexp.MustCompile(`^[a-f0-9]{64}$`)
var gitRevision = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func (s Source) validate() error {
	if len(s.Location) > 4096 || strings.ContainsAny(s.Location, "\x00\r\n") {
		return fmt.Errorf("invalid package source")
	}
	switch s.Kind {
	case "local":
		if !filepath.IsAbs(s.Location) || s.Revision != "" {
			return fmt.Errorf("local source requires an absolute directory and no Git revision")
		}
	case "git":
		if !gitRevision.MatchString(s.Revision) {
			return fmt.Errorf("Git sources require a full immutable commit id")
		}
		if filepath.IsAbs(s.Location) {
			return nil
		}
		u, err := url.Parse(s.Location)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("remote Git source must be HTTPS without credentials, query or fragment")
		}
	default:
		return fmt.Errorf("source kind must be local or git")
	}
	return nil
}

type boundedGitOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (w *boundedGitOutput) Write(p []byte) (int, error) {
	n := len(p)
	room := w.limit - w.Len()
	if room < n {
		w.overflow = true
		p = p[:max(0, room)]
	}
	w.Buffer.Write(p)
	return n, nil
}
func gitCommand(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "http.followRedirects=false", "-c", "protocol.ext.allow=never", "--literal-pathspecs"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "LANG=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second
	output := &boundedGitOutput{limit: limit}
	stderr := &boundedGitOutput{limit: 4096}
	cmd.Stdout = output
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Git source operation failed; check repository access and the exact commit")
	}
	if output.overflow {
		return nil, fmt.Errorf("Git source output exceeded its byte limit")
	}
	return append([]byte{}, output.Bytes()...), nil
}
func readGitPackage(parent context.Context, source Source) (Manifest, map[string]packageFile, error) {
	var empty Manifest
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "luna-package-git-")
	if err != nil {
		return empty, nil, err
	}
	defer os.RemoveAll(dir)
	initArgs := []string{"init", "--bare", "--template="}
	if len(source.Revision) == 64 {
		initArgs = append(initArgs, "--object-format=sha256")
	}
	initArgs = append(initArgs, dir)
	if _, err = gitCommand(ctx, dir, 4096, initArgs...); err != nil {
		return empty, nil, err
	}
	if _, err = gitCommand(ctx, dir, 4096, "fetch", "--depth=1", "--no-tags", "--", source.Location, source.Revision); err != nil {
		return empty, nil, err
	}
	head, err := gitCommand(ctx, dir, 256, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != source.Revision {
		return empty, nil, fmt.Errorf("Git source did not resolve to the requested immutable commit")
	}
	manifestTree, err := gitCommand(ctx, dir, 4096, "ls-tree", "-z", source.Revision, "--", ManifestName)
	if err != nil {
		return empty, nil, err
	}
	rows := bytes.Split(manifestTree, []byte{0})
	if len(rows) != 2 || len(rows[1]) != 0 {
		return empty, nil, fmt.Errorf("package manifest must be a regular Git blob")
	}
	parts := bytes.SplitN(rows[0], []byte{'\t'}, 2)
	if len(parts) != 2 || string(parts[1]) != ManifestName {
		return empty, nil, fmt.Errorf("invalid Git manifest entry")
	}
	fields := strings.Fields(string(parts[0]))
	if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !gitRevision.MatchString(fields[2]) {
		return empty, nil, fmt.Errorf("package manifest must not be a link or submodule")
	}
	raw, err := gitCommand(ctx, dir, MaxManifestBytes, "cat-file", "blob", fields[2])
	if err != nil {
		return empty, nil, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return empty, nil, err
	}
	if len(m.Files) == 0 {
		return m, map[string]packageFile{}, nil
	}
	args := []string{"ls-tree", "-z", source.Revision, "--"}
	args = append(args, m.Files...)
	tree, err := gitCommand(ctx, dir, MaxManifestBytes, args...)
	if err != nil {
		return empty, nil, err
	}
	type entry struct {
		object     string
		executable bool
	}
	entries := map[string]entry{}
	for _, line := range bytes.Split(tree, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return empty, nil, fmt.Errorf("invalid Git tree response")
		}
		fields := strings.Fields(string(parts[0]))
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") || !gitRevision.MatchString(fields[2]) {
			return empty, nil, fmt.Errorf("package sources cannot contain links or submodules")
		}
		entries[string(parts[1])] = entry{fields[2], fields[0] == "100755"}
	}
	files := map[string]packageFile{}
	total := int64(len(raw))
	for _, name := range m.Files {
		e, ok := entries[name]
		if !ok || name == receiptName {
			return empty, nil, fmt.Errorf("declared package file is missing or reserved")
		}
		sizeText, err := gitCommand(ctx, dir, 128, "cat-file", "-s", e.object)
		if err != nil {
			return empty, nil, err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(sizeText)), 10, 64)
		if err != nil || size < 0 || size > MaxPackageFileBytes || total+size > MaxPackageBytes {
			return empty, nil, fmt.Errorf("Git package payload exceeds its byte limits")
		}
		body, err := gitCommand(ctx, dir, MaxPackageFileBytes, "cat-file", "blob", e.object)
		if err != nil {
			return empty, nil, err
		}
		if int64(len(body)) != size {
			return empty, nil, fmt.Errorf("Git object size changed")
		}
		total += size
		files[name] = packageFile{body, e.executable}
	}
	return m, files, nil
}
