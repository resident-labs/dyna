//go:build !linux && !darwin

package store

import "syscall"

// ChangeTime is unknown here, so callers must not reuse cached snapshots.
func ChangeTime(*syscall.Stat_t) (int64, bool) { return 0, false }
