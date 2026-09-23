package main

import (
	"fmt"
	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
	"github.com/hashicorp/go-plugin"
	"os"
	"strings"
	"time"
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
	return "Luna · " + strings.ToUpper(strings.TrimSpace(in.Text)), nil
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
