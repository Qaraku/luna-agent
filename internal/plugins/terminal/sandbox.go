package terminal

import (
	"io"
	"os"
	"os/exec"
	"time"
)

const sandboxExecutable = "/usr/bin/bwrap"
const sandboxReady = "luna-sandbox-ready\n"
const sandboxScratchBytes = 512 << 20

// ready 由只读系统 shell 的固定 bootstrap 写入，随后关掉 FD 再启动模型程序。
// child-pid 并不代表挂载已完成，不能拿 Bubblewrap 的进程状态替代这个确认。
const sandboxBootstrap = "printf 'luna-sandbox-ready\\n' >&3 || exit 125; exec 3>&- 4>&-; exec /bin/sh -c \"$1\""

type isolatedCommand struct {
	cmd                              *exec.Cmd
	readyReader, readyWriter, filter *os.File
}

func (s *isolatedCommand) closeParentFiles() {
	if s.readyWriter != nil {
		_ = s.readyWriter.Close()
	}
	if s.filter != nil {
		_ = s.filter.Close()
	}
}
func (s *isolatedCommand) close() {
	s.closeParentFiles()
	if s.readyReader != nil {
		_ = s.readyReader.Close()
	}
}
func (s *isolatedCommand) ready() bool {
	_ = s.readyReader.SetReadDeadline(time.Now().Add(killGrace))
	var marker [len(sandboxReady)]byte
	_, err := io.ReadFull(s.readyReader, marker[:])
	return err == nil && string(marker[:]) == sandboxReady
}
