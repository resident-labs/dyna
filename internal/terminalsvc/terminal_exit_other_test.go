//go:build !linux

package terminalsvc

import "testing"

// waitProcessExit needs Linux pidfds. The background-job close check that
// uses it also reads /proc, which only Linux has.
func waitProcessExit(t *testing.T, pid, timeout int) {
	t.Helper()
	t.Skip("background job exit observation needs Linux pidfds")
}
