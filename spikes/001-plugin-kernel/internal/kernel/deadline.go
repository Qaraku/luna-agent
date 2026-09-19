package kernel

import (
	"context"
	"sync"
)

// The returned stop joins the watcher. Always stop BEFORE canceling the context:
// merely closing a ready channel leaves a select race against ctx.Done().
func watchStartup(ctx context.Context, kill func()) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(exited)
		select {
		case <-done:
		case <-ctx.Done():
			kill()
		}
	}()
	return func() { once.Do(func() { close(done) }); <-exited }
}
