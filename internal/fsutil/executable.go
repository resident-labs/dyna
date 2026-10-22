package fsutil

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// ErrExecutableChanged tells a long-running watcher that a new dyna build
// was installed. The watcher releases its lock and returns it; the command
// then starts the new build with ReexecInstalled.
var ErrExecutableChanged = errors.New("installed dyna binary changed")

// execFn is syscall.Exec; tests replace it.
var execFn = syscall.Exec

// ReexecInstalled replaces this process with the installed build at the same
// path, with the same arguments and environment. It returns only on error.
func ReexecInstalled() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	return execFn(strings.TrimSuffix(path, " (deleted)"), os.Args, os.Environ())
}

// ExecutableChanged compares the installed path with the running executable.
// On Linux /proc/self/exe still identifies the running inode after replacement.
func ExecutableChanged() (func() bool, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	path = strings.TrimSuffix(path, " (deleted)")
	running, err := os.Stat("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	return executableChanged(path, running), nil
}

func executableChanged(path string, running os.FileInfo) func() bool {
	return func() bool {
		installed, err := os.Stat(path)
		return os.IsNotExist(err) || (err == nil && !os.SameFile(running, installed))
	}
}
