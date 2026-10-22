//go:build linux

package procident

// StartTime returns pid's start time. A caller compares it with the time it
// recorded to tell this process from a later one that reuses the PID.
func StartTime(pid int) (uint64, error) {
	stat, err := Read("/proc", pid)
	if err != nil {
		return 0, err
	}
	return stat.StartTime, nil
}
