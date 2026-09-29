// Command v2 is the search plugin candidate that renders each matched line
// without its leading indentation.
//
// It shares v1's boundary and v1's mode handling — a query that names no mode is
// matched as a literal, and a query that asks for one is passed to fileread
// unchanged, so how a line is read is the same on both candidates; no path is
// interpreted, no symbolic link is followed, every cap is stated — and changes
// one thing in the returned text so a replacement is observable in the tool
// result: a matched line is rendered with its leading whitespace stripped, while
// its path, line number and remaining text are unchanged.
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
	return fileread.Search(in.Path, in.Query, fileread.SearchOptions{MaxMatches: in.MaxMatches, MaxLineBytes: in.MaxLineBytes, MaxFiles: in.MaxFiles, MaxFileBytes: in.MaxFileBytes, TrimIndent: true, Mode: in.Mode})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
