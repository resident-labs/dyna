package fsutil

import (
	"os"
	"strings"
	"syscall"
)

// execFn is syscall.Exec; tests replace it.
var execFn = syscall.Exec

// InstalledExecutable returns the path this process was started from, which an
// update replaces atomically. Unlike /proc/<pid>/exe it names the new build
// once the file is replaced, so a process started from it still runs.
func InstalledExecutable() (string, error) {
	path, err := os.Executable()
	return strings.TrimSuffix(path, " (deleted)"), err
}

// ReexecInstalled replaces this process with the installed build at the same
// path, with the same arguments and environment. It returns only on error.
func ReexecInstalled() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	return execFn(strings.TrimSuffix(path, " (deleted)"), os.Args, os.Environ())
}
