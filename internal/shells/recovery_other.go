//go:build !linux

package shells

import "errors"

// Exited-supervisor recovery requires a process handle that pins identity
// while checking the start time, which this system does not offer.
func openSupervisor(pid int) (supervisorHandle, error) {
	return nil, errors.New("exited shell supervisor recovery is not supported on this system")
}
