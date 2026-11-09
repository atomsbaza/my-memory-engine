//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockRoot is the Windows counterpart of flock_unix.go: an exclusive
// LockFileEx byte-range lock on <root>/.memory/.lock serialises store
// writers within one machine. Semantics mirror flock(LOCK_EX): the
// acquire blocks until the lock is free, and the kernel releases the
// lock when the handle (or the whole process) dies, so a crashed holder
// can never wedge the store (NFR-OPS-01).
func lockRoot(root string) (func(), error) {
	dir := filepath.Join(root, ".memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: lock %s: %w", dir, err)
	}
	// os.OpenFile would also work, but CreateFile keeps the handle in
	// synchronous mode with no CRT buffering, which is all LockFileEx needs.
	path, err := windows.UTF16PtrFromString(filepath.Join(dir, ".lock"))
	if err != nil {
		return nil, fmt.Errorf("store: lock path: %w", err)
	}
	h, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("store: open %s\\.lock: %w", dir, err)
	}
	// Lock byte 0 of the (content-free) lock file. Every contender locks
	// the same byte, so exactly one writer proceeds at a time.
	var ol windows.Overlapped
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("store: lock %s\\.lock: %w", dir, err)
	}
	return func() {
		defer windows.CloseHandle(h)
		if err := windows.UnlockFileEx(h, 0, 1, 0, &ol); err != nil {
			// Unlocking a handle we still hold cannot fail in practice;
			// report loudly rather than leaking a silently-held lock.
			fmt.Fprintf(os.Stderr, "store: unlock %s\\.lock: %v\n", dir, err)
		}
	}, nil
}
