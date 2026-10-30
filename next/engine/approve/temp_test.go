package approve

import (
	"os"
	"testing"
)

// Keep test stores off a busy disk when Linux shared memory is available.
func TestMain(m *testing.M) {
	if dir, err := os.MkdirTemp("/dev/shm", "dyna-test-"); err == nil {
		os.Setenv("TMPDIR", dir)
		code := m.Run()
		os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}
