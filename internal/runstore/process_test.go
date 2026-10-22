package runstore

import (
	"fmt"
	"syscall"
	"testing"
)

func TestProcessAlivePreservesPermissionDeniedProcesses(t *testing.T) {
	for _, tc := range []struct {
		name string
		pid  int
		err  error
		want bool
	}{
		{"running", 42, nil, true},
		{"permission denied", 42, syscall.EPERM, true},
		{"wrapped permission denied", 42, fmt.Errorf("probe: %w", syscall.EPERM), true},
		{"gone", 42, syscall.ESRCH, false},
		{"zero", 0, nil, false},
		{"negative", -1, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := processAlive(tc.pid, func(pid int, sig syscall.Signal) error {
				calls++
				if pid != tc.pid || sig != 0 {
					t.Fatal("invalid liveness probe")
				}
				return tc.err
			})
			if got != tc.want {
				t.Fatalf("alive=%v; want %v", got, tc.want)
			}
			if tc.pid <= 0 && calls != 0 {
				t.Fatal("invalid PID reached signal probe")
			}
		})
	}
}
