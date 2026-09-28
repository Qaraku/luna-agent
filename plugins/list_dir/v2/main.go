// Command v2 is the directory-listing plugin candidate that reports exact byte
// counts.
//
// It shares v1's boundary — one level unless the host sends a depth, no path is
// interpreted, no symbolic link is followed, every cap is stated — and changes
// one thing in the returned text so a replacement is observable in the tool
// result: a file's size is the raw byte count instead of a human-readable unit.
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
	return pluginprotocol.Metadata{Version: "v2", PID: os.Getpid(), Protocol: 1}, nil
}
func (tool) Invoke(in pluginprotocol.Input) (string, error) {
	if in.DelayMS < 0 || in.DelayMS > pluginprotocol.MaxDelayMS {
		return "", fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	return fileread.List(in.Path, fileread.ListOptions{Depth: in.Depth, MaxEntries: in.MaxEntries, MaxLineBytes: in.MaxLineBytes, MaxScanned: in.MaxScanned, ExactBytes: true})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
