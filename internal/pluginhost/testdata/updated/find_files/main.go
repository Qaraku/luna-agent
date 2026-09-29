// Command v2 is the name-search plugin candidate that renders every file size
// as an exact byte count.
//
// It shares v1's boundary — the glob is matched against one entry name, no path
// is interpreted, no symbolic link is followed, every cap is stated — and
// changes one thing in the returned text so a replacement is observable in the
// tool result: the size column is the same measured number in bytes instead of
// a human-readable unit, while the matched paths, their order and their kinds
// are unchanged.
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
	return pluginprotocol.Metadata{Version: "2.0.0", PID: os.Getpid(), Protocol: 1}, nil
}
func (tool) Invoke(in pluginprotocol.Input) (string, error) {
	if in.DelayMS < 0 || in.DelayMS > pluginprotocol.MaxDelayMS {
		return "", fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	return fileread.Find(in.Path, in.Pattern, fileread.FindOptions{MaxPaths: in.MaxPaths, MaxLineBytes: in.MaxLineBytes, MaxEntries: in.MaxScanned, ExactBytes: true})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
