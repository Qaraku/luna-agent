//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package datalifecycle

import (
	"errors"
	"os"
)

func openLocked(string) (*os.File, error) {
	return nil, errors.New("private data lifecycle locking is unsupported on this platform")
}
