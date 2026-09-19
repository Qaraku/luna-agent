package main

import (
	"fmt"
	"github.com/hashicorp/go-plugin"
	"luna-plugin-demo/internal/protocol"
	"os"
	"strings"
	"time"
)

type tool struct{}

func (tool) Metadata() (protocol.Metadata, error) {
	return protocol.Metadata{Version: "v2", PID: os.Getpid(), Protocol: 1}, nil
}
func (tool) Invoke(in protocol.Input) (string, error) {
	if in.DelayMS < 0 || in.DelayMS > 3000 {
		return "", fmt.Errorf("delay_ms must be 0..3000")
	}
	time.Sleep(time.Duration(in.DelayMS) * time.Millisecond)
	return "Luna · " + strings.ToUpper(strings.TrimSpace(in.Text)), nil
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: protocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &protocol.ToolPlugin{Impl: tool{}}}})
}
