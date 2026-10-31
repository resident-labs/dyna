package main

import (
	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

func roomLimits(limits room.Limits) (contract.RoomLimits, []contract.RoomLimitGap) {
	var out contract.RoomLimits
	if !limits.MemoryHigh.Off {
		out.MemBytes = limits.MemoryHigh.Value
	}
	var warnings []contract.RoomLimitGap
	if (limits.MemoryMax.Off || limits.MemoryMax.Value > 0) && !limits.MemoryHigh.Off && limits.MemoryHigh.Value == 0 {
		warnings = append(warnings, contract.RoomLimitMemoryMaxWithoutHigh)
	}
	return out, warnings
}
