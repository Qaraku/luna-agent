package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"luna-plugin-demo/internal/httpapi"
	"luna-plugin-demo/internal/kernel"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "literal loopback listen address")
	flag.Parse()
	listener, err := httpapi.Listen(*addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	k, err := kernel.New(ctx, root)
	if err != nil {
		return err
	}
	defer k.Close()
	server := &http.Server{Handler: httpapi.New(k, listener.Addr().String(), filepath.Join(root, "web")), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 75 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("LISTEN_URL=http://%s\n", listener.Addr())
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
		}
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
