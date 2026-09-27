// Command v2 is the file-read plugin candidate that normalizes line endings.
//
// It shares v1's boundary — the path is host-validated and never interpreted
// here, the size cap is refused rather than truncated, and binary content is
// refused — and changes the returned text so a replacement is observable in the
// tool result: CRLF and lone CR become LF.
//
// It also shares v1's line range: StartLine and MaxLines select the same lines
// as v1 selects, so the header states the same range, and only the file text
// below that header is rewritten. A candidate may differ in what it returns,
// but not in whether it honors a documented parameter — a replacement that
// ignored a range would answer the whole-file question the model did not ask.
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
	if in.DelayMS < 0 || in.DelayMS > pluginprotocol.MaxDelayMS {
		return "", fmt.Errorf("delay_ms must be 0..%d", pluginprotocol.MaxDelayMS)
	}
	if err := fileread.ValidateRange(in.StartLine, in.MaxLines); err != nil {
		return "", err
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	// ReadRangeParts keeps the two halves apart: the header is about which
	// lines these are, and only the text is this candidate's to rewrite.
	header, content, err := fileread.ReadRangeParts(in.Path, fileread.RangeOptions{StartLine: in.StartLine, MaxLines: in.MaxLines, MaxBytes: in.MaxBytes})
	if err != nil {
		return "", err
	}
	return header + strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"), nil
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
