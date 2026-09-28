// Command v1 is the search plugin candidate: it looks for one query in the file
// or directory the host validated.
//
// It never interprets a path. Path arrives absolute and already checked against
// the read root by the host, and Query plus MaxMatches/MaxLineBytes/MaxFiles/
// MaxFileBytes carry the query and the caps, while Mode says how the query is to
// be read — an empty Mode is the literal default, which is what a call that names
// no mode gets. The search is literal unless the call explicitly asked for a
// pattern — the query is data, so nothing in it is a pattern — it never follows a
// symbolic link, so its walk cannot leave the read root, and it states every cap
// it reached instead of cutting the answer silently (see internal/fileread).
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
	return fileread.Search(in.Path, in.Query, fileread.SearchOptions{MaxMatches: in.MaxMatches, MaxLineBytes: in.MaxLineBytes, MaxFiles: in.MaxFiles, MaxFileBytes: in.MaxFileBytes, Mode: in.Mode})
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
