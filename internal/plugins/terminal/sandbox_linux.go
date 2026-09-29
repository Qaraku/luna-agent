//go:build linux && (amd64 || arm64)

package terminal

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/Qaraku/luna-agent/internal/netbridge"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"golang.org/x/sys/unix"
)

var sandboxRuntimeRoots = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"}

func sandboxRoots(roots []string) ([]string, error) {
	reserved := append(append([]string(nil), sandboxRuntimeRoots...), "/etc", "/proc", "/sys", "/dev", "/run", "/tmp/home")
	home, _ := os.UserHomeDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if home != "" {
		for _, system := range sandboxRuntimeRoots {
			real, err := filepath.EvalSymlinks(system)
			if err == nil && (fileread.Within(real, home) || fileread.Within(home, real)) {
				return nil, plugin.Unavailable(fmt.Errorf("host home overlaps a system runtime mount; terminal isolation cannot safely start"))
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, root := range roots {
		resolved, err := fileread.ResolveDirInRoots([]string{root}, ".")
		if errors.Is(err, fileread.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		path := resolved.Path
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("terminal working directories must be absolute")
		}
		for _, special := range reserved {
			if fileread.Within(path, special) || fileread.Within(special, path) {
				return nil, fmt.Errorf("terminal working directory overlaps reserved sandbox path %q; choose a project directory", special)
			}
		}
		if home != "" && fileread.Within(path, home) {
			return nil, fmt.Errorf("terminal will not mount the whole host home directory; choose a project directory")
		}
		if !seen[path] {
			out = append(out, path)
			seen[path] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("none of this run's working directories exists")
	}
	return out, nil
}

func prepareSandbox(executable string, roots []string, cwd, command string) (*isolatedCommand, error) {
	return prepareSandboxWithAccess(context.Background(), executable, roots, cwd, command, sandboxAccess{Read: true})
}

func prepareSandboxWithAccess(ctx context.Context, executable string, roots []string, cwd, command string, access sandboxAccess) (*isolatedCommand, error) {
	var mounted []string
	var err error
	if access.Read {
		mounted, err = sandboxRoots(roots)
	}
	if err != nil {
		return nil, err
	}
	if executable == "" {
		executable = sandboxExecutable
	}
	readonly := []ReadOnlyMount{}
	targets := map[string]bool{}
	for _, mount := range access.Readonly {
		if !regexp.MustCompile(`^/run/luna/[a-z][a-z0-9-]{0,31}$`).MatchString(mount.Target) || targets[mount.Target] {
			return nil, fmt.Errorf("invalid or duplicate trusted resource mount target")
		}
		targets[mount.Target] = true
		paths, err := sandboxRoots([]string{mount.Source})
		if err != nil {
			return nil, err
		}
		readonly = append(readonly, ReadOnlyMount{Source: paths[0], Target: mount.Target})
	}
	var writable []string
	for _, root := range access.WriteRoots {
		resolved, err := fileread.ResolveDirInRoots([]string{root}, ".")
		if err != nil {
			return nil, err
		}
		contained := false
		for _, base := range mounted {
			if fileread.Within(base, resolved.Path) {
				contained = true
				break
			}
		}
		if !contained {
			return nil, fmt.Errorf("writable mount must remain inside this command's readable project roots")
		}
		writable = append(writable, resolved.Path)
	}
	filter, err := sandboxFilter()
	if err != nil {
		return nil, plugin.Unavailable(fmt.Errorf("prepare terminal isolation: %w", err))
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		filter.Close()
		return nil, plugin.Unavailable(fmt.Errorf("prepare terminal isolation status: %w", err))
	}
	isolation := &isolatedCommand{readyReader: readyR, readyWriter: readyW, filter: filter}
	args := []string{
		"--unshare-all", "--unshare-user", "--disable-userns", "--cap-drop", "ALL",
		"--new-session", "--die-with-parent", "--clearenv",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/tmp/home", "--setenv", "TMPDIR", "/tmp",
	}
	for _, path := range sandboxRuntimeRoots {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--size", fmt.Sprint(sandboxScratchBytes), "--tmpfs", "/tmp", "--dir", "/tmp/home", "--dir", "/tmp/work")
	for _, path := range mounted {
		args = append(args, "--ro-bind", path, path)
	}
	for _, path := range writable {
		args = append(args, "--bind", path, path)
	}
	for _, mount := range readonly {
		args = append(args, "--ro-bind", mount.Source, mount.Target)
		for _, base := range mounted {
			if fileread.Within(base, mount.Source) {
				args = append(args, "--ro-bind", mount.Source, mount.Source)
				break
			}
		}
	}
	bootstrap := sandboxBootstrap
	extra := []*os.File{readyW, filter}
	if access.Network {
		helper, err := os.Executable()
		if err != nil {
			isolation.close()
			return nil, plugin.Unavailable(fmt.Errorf("network helper is unavailable"))
		}
		helper, err = filepath.EvalSymlinks(helper)
		if err != nil {
			isolation.close()
			return nil, plugin.Unavailable(fmt.Errorf("network helper is unavailable"))
		}
		file, stop, err := startNetworkBridge(ctx, access.Dial)
		if err != nil {
			isolation.close()
			return nil, plugin.Unavailable(err)
		}
		isolation.networkFile = file
		isolation.stopNetwork = stop
		extra = append(extra, file)
		args = append(args, "--ro-bind", helper, "/.luna-network-helper")
		for _, path := range []string{"/etc/ssl/certs", "/etc/ssl/cert.pem", "/etc/pki/tls/certs"} {
			if _, err := os.Stat(path); err == nil {
				args = append(args, "--ro-bind", path, path)
			}
		}
		bootstrap = "exec 4>&-; exec /.luna-network-helper --luna-network-helper \"$1\""
	}
	args = append(args, "--remount-ro", "/proc", "--remount-ro", "/dev", "--remount-ro", "/", "--seccomp", "4", "--chdir", cwd, "--", "/bin/sh", "-c", bootstrap, "luna-sandbox", command)
	cmd := exec.Command(executable, args...)
	cmd.Dir = "/"
	cmd.Env = commandEnv()
	cmd.ExtraFiles = extra
	isolation.cmd = cmd
	return isolation, nil
}

// 网络 namespace 不阻断通过已挂载路径连接宿主 Unix socket，因此拒绝 AF_UNIX
// socket。匿名 socketpair 保留。架构检查和 x32 拒绝避免切换 syscall ABI 绕过。
func sandboxFilter() (*os.File, error) {
	arch := uint32(unix.AUDIT_ARCH_X86_64)
	if runtime.GOARCH == "arm64" {
		arch = unix.AUDIT_ARCH_AARCH64
	}
	program := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SOCKET), Jf: 3},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_UNIX, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	file, err := os.CreateTemp("", "luna-terminal-filter-")
	if err != nil {
		return nil, err
	}
	// 立即解除名字，只传 FD；不会把可替换的过滤器文件留给模型进程。
	if err := os.Remove(file.Name()); err != nil {
		file.Close()
		return nil, err
	}
	if err := binary.Write(file, binary.LittleEndian, program); err != nil {
		file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func startNetworkBridge(parent context.Context, dial netbridge.DialFunc) (*os.File, func(), error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("create isolated network channel: %w", err)
	}
	hostFile := os.NewFile(uintptr(pair[0]), "network-host")
	childFile := os.NewFile(uintptr(pair[1]), "network-child")
	wire, err := net.FileConn(hostFile)
	hostFile.Close()
	if err != nil {
		childFile.Close()
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() { netbridge.Serve(ctx, wire, dial); close(done) }()
	return childFile, func() { cancel(); wire.Close(); <-done }, nil
}
