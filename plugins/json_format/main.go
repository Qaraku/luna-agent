// JSON 工具在独立进程中格式化或压缩文本，不读写用户文件，也不调用模型。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
	"github.com/hashicorp/go-plugin"
)

const pluginVersion = "1.0.0"
const prettyIndent = "  "
const maxInputBytes = 16 << 10
const maxOutputBytes = 64 << 10
const maxDepth = 64

type tool struct{}

func (tool) Metadata() (pluginprotocol.Metadata, error) {
	return pluginprotocol.Metadata{Version: pluginVersion, PID: os.Getpid(), Protocol: 1}, nil
}
func (tool) Invoke(in pluginprotocol.Input) (string, error) {
	if len(in.Text) > maxInputBytes {
		return "", fmt.Errorf("JSON input exceeds %d bytes", maxInputBytes)
	}
	mode := in.Mode
	if mode == "" {
		mode = "pretty"
	}
	if mode != "pretty" && mode != "compact" {
		return "", fmt.Errorf("mode must be pretty or compact")
	}
	raw := []byte(in.Text)
	if !json.Valid(raw) {
		return "", fmt.Errorf("text must contain exactly one valid JSON value")
	}
	decoder := json.NewDecoder(strings.NewReader(in.Text))
	decoder.UseNumber()
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			if depth > maxDepth {
				return "", fmt.Errorf("JSON nesting exceeds %d levels", maxDepth)
			}
		}
	}
	var out bytes.Buffer
	var err error
	if mode == "compact" {
		err = json.Compact(&out, raw)
	} else {
		err = json.Indent(&out, raw, "", prettyIndent)
	}
	if err != nil {
		return "", err
	}
	if out.Len() > maxOutputBytes {
		return "", fmt.Errorf("formatted JSON exceeds %d bytes; use compact mode", maxOutputBytes)
	}
	return out.String(), nil
}
func main() {
	plugin.Serve(&plugin.ServeConfig{HandshakeConfig: pluginprotocol.Handshake, Plugins: map[string]plugin.Plugin{"tool": &pluginprotocol.ToolPlugin{Impl: tool{}}}})
}
