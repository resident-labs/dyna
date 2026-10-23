package fsutil

import (
	"fmt"
	"os"
	"runtime"
)

// RunningExecutable returns the binary this process started with. Linux
// workers use the parent process inode so an installed update cannot run new
// workers against the old engine schema while a voice call delays restart.
func RunningExecutable() (string, error) {
	if runtime.GOOS != "linux" {
		return os.Executable()
	}
	path := fmt.Sprintf("/proc/%d/exe", os.Getpid())
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}
