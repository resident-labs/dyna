package main

import (
	"testing"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

func TestRoomLimitsWarnWhenOnlyMemoryMaxIsSet(t *testing.T) {
	limits, warnings := roomLimits(room.Limits{MemoryMax: room.Limit{Value: 4096}})
	if limits.MemBytes != 0 || len(warnings) != 1 || warnings[0] != contract.RoomLimitMemoryMaxWithoutHigh {
		t.Fatalf("unexpected limits and warnings: %+v %+v", limits, warnings)
	}
	limits, warnings = roomLimits(room.Limits{MemoryHigh: room.Limit{Value: 2048}, MemoryMax: room.Limit{Value: 4096}})
	if limits.MemBytes != 2048 || len(warnings) != 0 {
		t.Fatalf("unexpected limits and warnings: %+v %+v", limits, warnings)
	}
}
