//go:build !linux

package roomsvc

import "errors"

// Without pidfds a runner cannot be watched, so its session keeps
// every-tick member scans.
type runnerHandle struct{}

func openRunner(int) (runnerHandle, error) {
	return runnerHandle{}, errors.New("runner watches need Linux pidfds")
}

func (runnerHandle) wait(*quitter) bool { return false }
func (runnerHandle) close()             {}

type quitter struct{}

func (*quitter) signal() {}
func (*quitter) close()  {}
