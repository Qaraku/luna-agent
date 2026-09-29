package netbridge

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func pairedBridge(t *testing.T, dial DialFunc) (*Client, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	host, child := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Serve(ctx, host, dial); close(done) }()
	client := NewClient(child)
	t.Cleanup(func() {
		client.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("bridge did not stop")
		}
	})
	return client, cancel, done
}
func TestBridgeCarriesConcurrentBoundedStreams(t *testing.T) {
	dial := func(context.Context, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); io.Copy(b, b) }()
		return a, nil
	}
	client, _, _ := pairedBridge(t, dial)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := client.DialContext(ctx, "tcp", "example.test:443")
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			payload := strings.Repeat("x", ChunkBytes*2+3)
			go func() {
				_, err := io.WriteString(conn, payload)
				if err != nil {
					t.Error(err)
				}
			}()
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil {
				t.Error(err)
				return
			}
			if string(got) != payload {
				t.Error("stream content changed")
			}
		}()
	}
	wg.Wait()
}
func TestBridgeNeverDialsPrivateTargets(t *testing.T) {
	client, _, _ := pairedBridge(t, nil)
	for _, target := range []string{"127.0.0.1:3210", "[::ffff:127.0.0.1]:3210", "10.0.0.1:80", "169.254.169.254:80"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := client.DialContext(ctx, "tcp", target)
		cancel()
		if err == nil {
			conn.Close()
			t.Errorf("allowed private target %s", target)
		}
	}
}
func TestBridgeCancellationClosesPendingRead(t *testing.T) {
	remoteClosed := make(chan struct{})
	dial := func(context.Context, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer close(remoteClosed); defer b.Close(); var data [1]byte; b.Read(data[:]) }()
		return a, nil
	}
	client, cancel, done := pairedBridge(t, dial)
	conn, err := client.DialContext(context.Background(), "tcp", "example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { var data [1]byte; _, err := conn.Read(data[:]); read <- err }()
	cancel()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("read stuck")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("host stuck")
	}
	select {
	case <-remoteClosed:
	case <-time.After(time.Second):
		t.Fatal("remote socket leaked")
	}
}
