//go:build unix

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockRoot takes an exclusive flock on <root>/.memory/.lock and returns an
// unlock function, serialising store writers within one machine (the same
// approach as the Python engine's locks; there is no cross-machine or
// network coordination — sync is a later phase). Blocking is bounded by the
// kernel: a crashed holder releases the lock when its fd dies.
func lockRoot(root string) (func(), error) {
	dir := filepath.Join(root, ".memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: lock %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: open %s/.lock: %w", dir, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("store: flock %s/.lock: %w", dir, err)
	}
	return func() {
		// Release is best-effort: the write has already completed, and a
		// held fd is released by close (or by process death) regardless.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
