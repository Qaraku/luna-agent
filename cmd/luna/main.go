package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Qaraku/luna-agent/internal/agent"
	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/httpapi"
	"github.com/Qaraku/luna-agent/internal/memory"
	"github.com/Qaraku/luna-agent/internal/pluginhost"
	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/uiplugin"
)

// rootFromExecutable assumes the conventional layout where the built binary
// lives at <root>/.runtime/luna.
func rootFromExecutable(executable string) string { return filepath.Dir(filepath.Dir(executable)) }

// checkAssets reports why dir cannot serve the runtime. A usable root holds a
// web/index.html file and a plugins directory.
func checkAssets(dir string) error {
	index, err := os.Stat(filepath.Join(dir, "web", "index.html"))
	if err != nil {
		return fmt.Errorf("web/index.html: %w", err)
	}
	if index.IsDir() {
		return errors.New("web/index.html is a directory")
	}
	plugins, err := os.Stat(filepath.Join(dir, "plugins"))
	if err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	if !plugins.IsDir() {
		return errors.New("plugins exists but is not a directory")
	}
	return nil
}

// hasAssets reports whether dir is a usable root.
func hasAssets(dir string) bool { return dir != "" && checkAssets(dir) == nil }

// workingDirOrEmpty returns the process working directory. That directory can be
// removed or renamed out from under the process, which only costs the weakest root
// candidate, so the failure becomes an empty candidate instead of a fatal error.
func workingDirOrEmpty() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// resolveRoot locates the directory holding web/ and plugins/. An explicit -root
// wins and must be usable. Otherwise the executable's assumed grandparent is tried
// first, then the working directory, which is the candidate that makes
// `go run ./cmd/luna` work from a fresh checkout.
func resolveRoot(explicit, executable, workingDir string) (string, error) {
	if explicit != "" {
		if err := checkAssets(explicit); err != nil {
			return "", fmt.Errorf("root %q is not usable: %w", explicit, err)
		}
		return explicit, nil
	}
	var tried []string
	for _, candidate := range []string{rootFromExecutable(executable), workingDir} {
		if candidate == "" || slices.Contains(tried, candidate) {
			continue
		}
		if hasAssets(candidate) {
			return candidate, nil
		}
		tried = append(tried, candidate)
	}
	if len(tried) == 0 {
		return "", errors.New("cannot locate web/ and plugins/; pass -root")
	}
	return "", fmt.Errorf("cannot locate web/ and plugins/; tried %s; pass -root", strings.Join(tried, ", "))
}

// sessionsDir resolves the session directory. An explicit value wins; otherwise
// sessions live under the resolved root, where .runtime/ is already ignored by
// git.
func sessionsDir(root, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(root, ".runtime", "sessions")
}

// memoryFile resolves the append-only memory file, the same way: an explicit
// path wins, otherwise the file lives under the resolved root next to the
// sessions.
func memoryFile(root, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(root, ".runtime", "memory.jsonl")
}

// uiPluginsDir resolves the runtime UI plugin directory. It always lives under
// the resolved root and has no flag: a browser is only ever served plugins from
// the checkout that is running, never from a path a request asked for.
func uiPluginsDir(root string) string {
	return filepath.Join(root, "plugins", uiplugin.Dir)
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "literal loopback listen address")
	rootFlag := flag.String("root", "", "repository root holding web/ and plugins/ (default: auto-detect)")
	readRoot := flag.String("read-root", "", "directory luna_read_file may read inside (default: the resolved root)")
	readLimit := flag.Int("read-limit", 0, "single-read cap in bytes for luna_read_file (default: 262144)")
	sessionsFlag := flag.String("sessions-dir", "", "directory holding the append-only session files (default: <root>/.runtime/sessions/)")
	memoryFlag := flag.String("memory-file", "", "file holding the append-only memory facts (default: <root>/.runtime/memory.jsonl)")
	flag.Parse()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	root, err := resolveRoot(*rootFlag, executable, workingDirOrEmpty())
	if err != nil {
		return err
	}
	sessions, err := store.Open(sessionsDir(root, *sessionsFlag))
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	// Memory is core state, not a plugin: the tool that writes it and the
	// injection that reads it both live in the core, backed by this file.
	facts, err := memory.Open(memoryFile(root, *memoryFlag))
	if err != nil {
		return fmt.Errorf("open memory store: %w", err)
	}
	listener, err := httpapi.Listen(*addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	plugins, err := pluginhost.New(ctx, root, pluginhost.Options{ReadRoot: *readRoot, ReadLimit: *readLimit})
	if err != nil {
		return fmt.Errorf("start plugin host: %w", err)
	}
	defer plugins.Close()
	// The store is both sides of the conversation: history is read from it and
	// the transcript of every run is appended to it. The same pattern holds for
	// memory, whose read side is the system prompt and whose write side is the
	// host-native luna_remember tool.
	runner, err := agent.NewOpenAIRunner(ctx, cfg, plugins, plugins, agent.WithHistory(sessions), agent.WithTranscript(sessions), agent.WithMemory(facts))
	if err != nil {
		return fmt.Errorf("construct Eino agent: %w", err)
	}
	bound := listener.Addr().String()
	handler := httpapi.New(plugins, runner, sessions, httpapi.Info{BoundHost: bound, Model: cfg.Model, ProviderHost: cfg.ProviderHost, WebDir: filepath.Join(root, "web"), UIPluginsDir: uiPluginsDir(root)}, httpapi.WithMemory(facts))
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 70 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		done <- err
	}()
	fmt.Printf("LISTEN_URL=http://%s\n", bound)
	fmt.Printf("ROOT=%s\n", root)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-done
	}
}

func main() {
	if err := run(); err != nil {
		log.Printf("luna: %v", err)
		os.Exit(1)
	}
}
