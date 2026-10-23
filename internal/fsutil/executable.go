package fsutil

import (
	"os"
	"strings"
	"syscall"
)

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
