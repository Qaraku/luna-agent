// 此工具从宿主校验过的输入执行操作，源码可独立重载，无需重启 Luna。
//
// It never interprets a path. Path arrives absolute and already checked against
// the read root by the host, and Pattern plus MaxPaths/MaxLineBytes/MaxScanned
// carry the glob and the caps. The glob is matched against one entry name, so
// nothing in it addresses a path; no symbolic link is followed, so the walk
// cannot leave the read root; and every cap it reached is stated instead of the
// answer being cut silently (see internal/fileread).
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
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	return fileread.Find(in.Path, in.Pattern, fileread.FindOptions{MaxPaths: in.MaxPaths, MaxLineBytes: in.MaxLineBytes, MaxEntries: in.MaxScanned})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
