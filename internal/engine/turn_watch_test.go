package engine

import (
	"testing"

	"dyna-agent/internal/sysload"
)

// The divisor counts only sessions with a live cgroup scope, and the memory is
// theirs alone: the machine, engine, agents and computers groups are neither
// counted nor added.
func TestLiveAgentMemoryCountsOnlyLiveSessionScopes(t *testing.T) {
	groups := []sysload.Group{
		{Name: "machine", Memory: 30 << 30},
		{Name: "engine", Memory: 1 << 30},
		{Name: "agents", Memory: 9 << 30},
		{Name: "computers", Memory: 2 << 30},
		{Name: "session:ses_a", Memory: 3 << 30},
		{Name: "session:ses_b", Memory: 1 << 30},
	}
	if bytes, agents := liveAgentMemory(groups); bytes != 4<<30 || agents != 2 {
		t.Fatalf("memory %d over %d agents, want 4 GiB over 2", bytes, agents)
	}
	if bytes, agents := liveAgentMemory(groups[:4]); bytes != 0 || agents != 0 {
		t.Fatalf("with no live session the inventory must be empty, got %d over %d", bytes, agents)
	}
}
