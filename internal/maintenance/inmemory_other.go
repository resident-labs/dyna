//go:build !linux

package maintenance

import "syscall"

// inMemory reports false: this build has no check for an in-memory file system
// type, so every path is judged by the absolute minimum free space.
func inMemory(*syscall.Statfs_t) bool { return false }
