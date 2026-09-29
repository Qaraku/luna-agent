// 此工具从宿主校验过的输入执行操作，源码可独立重载，无需重启 Luna。
//
// It never interprets a path. Path arrives absolute and already checked
// against the read root by the host, and MaxBytes carries the host's cap. Even
// so the plugin refuses content it cannot return honestly: more than MaxBytes
// bytes (never a truncation) and binary content (any NUL byte in the bytes read
// — see internal/fileread).
//
// StartLine and MaxLines are the optional line range of the read, and they are
// the documented shape of a read here: with neither set the whole file is
// returned on the whole-file terms, and with either set one bounded window of
// it comes back with the lines it covers and the lines it left stated. A
// implementation that ignored them would be a implementation that answers a different
// question than the one the model asked.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
	"github.com/hashicorp/go-plugin"
)

type tool struct{}

func (tool) Metadata() (pluginprotocol.Metadata, error) {
	return pluginprotocol.Metadata{Version: "1.0.0", PID: os.Getpid(), Protocol: 1}, nil
}
func (tool) Invoke(in pluginprotocol.Input) (string, error) {
	if in.DelayMS < 0 || in.DelayMS > pluginprotocol.MaxDelayMS {
		return "", fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	if err := fileread.ValidateRange(in.StartLine, in.MaxLines); err != nil {
		return "", err
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	return fileread.ReadRange(in.Path, fileread.RangeOptions{StartLine: in.StartLine, MaxLines: in.MaxLines, MaxBytes: in.MaxBytes})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
