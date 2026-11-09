//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// lockRoot is the Windows counterpart of flock_unix.go: an exclusive
// LockFileEx byte-range lock on <root>/.memory/.lock serialises store
// writers within one machine. Semantics mirror flock(LOCK_EX): the
// acquire blocks until the lock is free, and the kernel releases the
// lock when the handle (or the whole process) dies, so a crashed holder
// can never wedge the store (NFR-OPS-01).
//
// Implemented with raw syscalls instead of golang.org/x/sys/windows on
// purpose: x/sys/windows drags the `net` package into internal/store's
// dependency set, which AC-P1-10 (no network-capable deps) forbids.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// lockfileExclusiveLock is LOCKFILE_EXCLUSIVE_LOCK: without
// LOCKFILE_FAIL_IMMEDIATELY the acquire blocks, like flock(LOCK_EX).
const lockfileExclusiveLock = 0x00000002

// overlapped mirrors OVERLAPPED. For a synchronous handle LockFileEx
// only needs the zero value.
type overlapped struct {
	internal, internalHigh uint32
	offset, offsetHigh     uint32
	hEvent                 syscall.Handle
}

func lockRoot(root string) (func(), error) {
	dir := filepath.Join(root, ".memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: lock %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("store: open %s\\.lock: %w", dir, err)
	}
	h := syscall.Handle(f.Fd())
	// Lock byte 0 of the (content-free) lock file: every contender locks
	// the same byte, so exactly one writer proceeds at a time.
	var ol overlapped
	r1, _, errno := syscall.SyscallN(procLockFileEx.Addr(),
		uintptr(h), lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r1 == 0 {
		f.Close()
		return nil, fmt.Errorf("store: lock %s\\.lock: %w", dir, errno)
	}
	return func() {
		defer f.Close()
		var ol overlapped
		r1, _, errno := syscall.SyscallN(procUnlockFileEx.Addr(),
			uintptr(h), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
		if r1 == 0 {
			// Unlocking a handle we still hold cannot fail in practice;
			// report loudly rather than leaking a silently-held lock.
			fmt.Fprintf(os.Stderr, "store: unlock %s\\.lock: %v\n", dir, errno)
		}
	}, nil
}
