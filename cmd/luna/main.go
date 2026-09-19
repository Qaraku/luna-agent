package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"luna-agent/internal/agent"
	"luna-agent/internal/config"
	"luna-agent/internal/httpapi"
	"luna-agent/internal/pluginhost"
)

func rootFromExecutable(executable string) string { return filepath.Dir(filepath.Dir(executable)) }

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "literal loopback listen address")
	flag.Parse()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	root := rootFromExecutable(executable)
	listener, err := httpapi.Listen(*addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	plugins, err := pluginhost.New(ctx, root, pluginhost.Options{})
	if err != nil {
		return fmt.Errorf("start plugin host: %w", err)
	}
	defer plugins.Close()
	runner, err := agent.NewOpenAIRunner(ctx, cfg, plugins)
	if err != nil {
		return fmt.Errorf("construct Eino agent: %w", err)
	}
	bound := listener.Addr().String()
	handler := httpapi.New(plugins, runner, httpapi.Info{BoundHost: bound, Model: cfg.Model, ProviderHost: cfg.ProviderHost, WebDir: filepath.Join(root, "web")})
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
