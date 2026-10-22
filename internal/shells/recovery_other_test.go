//go:build !linux

package shells

import (
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/procident"
)

func TestRecoveryUnsupported(t *testing.T) {
	s := &Store{}
	if err := s.stopExitedSupervisor(Info{}); err != nil {
		t.Fatalf("no supervisor: %v", err)
	}
	err := s.stopExitedSupervisor(Info{Meta: Meta{Supervisor: os.Getpid()}})
	if err == nil || !strings.Contains(err.Error(), "not supported on this system") {
		t.Fatalf("recovery error = %v", err)
	}
	started, err := procident.StartTime(os.Getpid())
	if err != nil || started != 0 {
		t.Fatalf("optional identity = %d, %v; want none", started, err)
	}
}
