//go:build !unix && !windows

package store

import (
	"fmt"
	"runtime"
)

// lockRoot has no flock implementation on this platform. It fails loudly
// rather than letting concurrent writers through silently (NFR-OPS-01).
func lockRoot(root string) (func(), error) {
	return nil, fmt.Errorf("store: flock locking is not supported on %s — refusing to write concurrently", runtime.GOOS)
}
