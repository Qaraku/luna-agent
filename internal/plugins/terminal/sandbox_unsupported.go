//go:build !linux || (!amd64 && !arm64)

package terminal

import (
	"errors"
	"github.com/Qaraku/luna-agent/internal/plugin"
)

func prepareSandbox(string, []string, string, string) (*isolatedCommand, error) {
	return nil, plugin.Unavailable(errors.New("isolated terminal requires Linux amd64 or arm64 with Bubblewrap; no unsandboxed fallback is available"))
}
