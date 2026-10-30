package approve

import (
	"os"
	"runtime"
	"testing"
)

// Keep test stores off a busy disk when Linux shared memory is available.
func TestMain(m *testing.M) {
	if runtime.GOOS == "linux" {
		if dir, err := os.MkdirTemp("/dev/shm", "dyna-test-"); err == nil {
			if os.Setenv("TMPDIR", dir) == nil {
				code := m.Run()
				os.RemoveAll(dir)
				os.Exit(code)
			}
			os.RemoveAll(dir)
		}
	}
	os.Exit(m.Run())
}
