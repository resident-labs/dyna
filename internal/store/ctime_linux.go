package store

import "syscall"

// ChangeTime returns the inode change time and whether the platform supplies it.
func ChangeTime(st *syscall.Stat_t) (int64, bool) { return st.Ctim.Nano(), true }
