//go:build !linux

package subagent

import (
	"errors"
	"syscall"
)

func signalRunner(string, int, int, syscall.Signal) error {
	return errors.New("detached runner signals require Linux pidfds")
}
