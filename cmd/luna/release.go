package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Qaraku/luna-agent/internal/buildinfo"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/release"
)

func releaseOptions(ctx context.Context, root, cache string, tools []pluginhost.ToolSpec) (pluginhost.Options, bool, error) {
	opts := pluginhost.Options{Tools: tools}
	_, err := os.Lstat(filepath.Join(root, release.ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		if buildinfo.Commit != "unknown" {
			return opts, false, fmt.Errorf("packaged Luna requires matching release.json resources")
		}
		return opts, false, nil
	}
	if err != nil {
		return opts, false, err
	}
	m, err := release.Verify(ctx, root)
	if err != nil {
		return opts, false, err
	}
	if buildinfo.Commit != "unknown" && (m.Commit != buildinfo.Commit || m.Version != buildinfo.Version) {
		return opts, false, fmt.Errorf("program and release resources identify different builds")
	}
	opts.Prebuilt = map[string]pluginhost.PrebuiltBinary{}
	for _, tool := range tools {
		name := "plugins/bin/" + tool.Dir
		file, ok := m.File(name)
		if !ok {
			return opts, false, fmt.Errorf("release is missing registered tool %s", tool.Tool)
		}
		opts.Prebuilt[tool.Tool] = pluginhost.PrebuiltBinary{Path: filepath.Join(root, filepath.FromSlash(name)), SHA256: file.SHA256}
	}
	opts.RuntimeDir = filepath.Join(cache, "plugin-processes")
	return opts, true, nil
}
