package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestStartDirRequiresCallerDirectory(t *testing.T) {
	m := &Manager{}
	dir, err := m.startDir(StartOptions{}, room.Record{}, "")
	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("startDir without a supplied directory = %q, %v; want a working directory error", dir, err)
	}
}
