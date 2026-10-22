//go:build !linux

package procident

// StartTime reports no start time on a system without /proc. Identity is
// optional there, so a caller records none and keeps its plain PID probe; a
// check that cannot verify a process must fail closed.
func StartTime(pid int) (uint64, error) { return 0, nil }
