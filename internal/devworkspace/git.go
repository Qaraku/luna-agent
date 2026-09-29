package devworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("development Git output limit exceeded")
	}
	return b.Buffer.Write(p)
}

// git只用于本次创建的独立/临时仓库，从不对候选.git配置执行命令。
func git(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	fixed := []string{"-C", dir, "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false", "-c", "core.attributesFile=/dev/null", "-c", "commit.gpgSign=false", "-c", "color.ui=false"}
	cmd := exec.CommandContext(ctx, "git", append(fixed, args...)...)
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
	for _, key := range []string{"PATH", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	output := &boundedBuffer{limit: limit}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("isolated development Git operation failed: %w", err)
	}
	return output.Bytes(), nil
}
func initGit(ctx context.Context, dir string) error {
	if _, err := git(ctx, dir, 1024, "init", "-q", "--template="); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git/info"), 0700); err != nil {
		return err
	}
	// info/attributes的优先级高于候选树的.gitattributes；保持文件原始字节且不运行过滤器。
	if err := os.WriteFile(filepath.Join(dir, ".git/info/attributes"), []byte("* -text -filter -ident -working-tree-encoding\n"), 0600); err != nil {
		return err
	}
	if _, err := git(ctx, dir, 1024, "add", "-A", "-f", "--", "."); err != nil {
		return err
	}
	_, err := git(ctx, dir, 1024, "-c", "user.name=Luna development", "-c", "user.email=luna-dev@localhost", "commit", "-q", "--no-gpg-sign", "-m", "Initial development snapshot")
	return err
}
