//go:build linux

package maintenance

import "syscall"

// tmpfsMagic is the statfs file system type of a RAM-backed file system.
const tmpfsMagic = 0x01021994

// inMemory reports whether the file system of st lives in memory.
func inMemory(st *syscall.Statfs_t) bool { return uint64(st.Type) == tmpfsMagic }
