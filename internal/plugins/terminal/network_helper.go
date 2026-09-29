package terminal

import (
	"fmt"
	"github.com/Qaraku/luna-agent/internal/netbridge"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// NetworkHelper 必须在程序加载用户配置之前分派。它只使用沙箱继承的 IPC 与状态管道。
func NetworkHelper(args []string) (int, bool) {
	if len(args) < 2 || args[1] != "--luna-network-helper" {
		return 0, false
	}
	if len(args) != 3 {
		return 125, true
	}
	file := os.NewFile(5, "isolated-network-channel")
	wire, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return 125, true
	}
	if wire.LocalAddr().Network() != "unix" {
		wire.Close()
		return 125, true
	}
	client := netbridge.NewClient(wire)
	defer client.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 125, true
	}
	proxy := netbridge.NewProxy(client)
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 32 * 1024}
	done := make(chan struct{})
	go func() { server.Serve(listener); close(done) }()
	defer func() { client.Close(); proxy.Close(); server.Close(); listener.Close(); <-done }()
	ready := os.NewFile(3, "sandbox-ready")
	if _, err := ready.WriteString(sandboxReady); err != nil {
		ready.Close()
		return 125, true
	}
	ready.Close()
	proxyURL := "http://" + listener.Addr().String()
	command := exec.Command("/bin/sh", "-c", args[2])
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp/home", "TMPDIR=/tmp", "HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL, "ALL_PROXY=" + proxyURL, "http_proxy=" + proxyURL, "https_proxy=" + proxyURL, "all_proxy=" + proxyURL, "NO_PROXY=", "no_proxy="}
	if err := command.Run(); err != nil {
		if code, ok := err.(*exec.ExitError); ok {
			return code.ExitCode(), true
		}
		fmt.Fprintln(os.Stderr, "network command could not start: "+strconv.Itoa(127))
		return 127, true
	}
	return 0, true
}
