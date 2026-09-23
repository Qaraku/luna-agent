// Command v2 is the file-read plugin candidate that normalizes line endings.
//
// It shares v1's boundary — the path is host-validated and never interpreted
// here, the size cap is refused rather than truncated, and binary content is
// refused — and changes the returned text so a replacement is observable in the
// tool result: CRLF and lone CR become LF.
package main

import (
	"fmt"
	"os"
	"strings"
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
	if in.DelayMS < 0 || in.DelayMS > 3000 {
		return "", fmt.Errorf("delay_ms must be 0..3000")
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	content, err := fileread.Read(in.Path, in.MaxBytes)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"), nil
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
