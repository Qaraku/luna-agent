// Command v1 is the directory-listing plugin candidate: it renders one level of
// the directory the host validated.
//
// It never interprets a path. Path arrives absolute and already checked against
// the read root by the host, and MaxEntries/MaxLineBytes carry the host's caps.
// The plugin lists exactly one level — it never enters a subdirectory and never
// follows a symbolic link, so a listing cannot grow into a recursive walk of the
// read root — and it states every cap it reached instead of cutting the list
// silently (see internal/fileread).
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
	return pluginprotocol.Metadata{Version: "v1", PID: os.Getpid(), Protocol: 1}, nil
}
func (tool) Invoke(in pluginprotocol.Input) (string, error) {
	if in.DelayMS < 0 || in.DelayMS > pluginprotocol.MaxDelayMS {
		return "", fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	return fileread.List(in.Path, fileread.ListOptions{MaxEntries: in.MaxEntries, MaxLineBytes: in.MaxLineBytes})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
