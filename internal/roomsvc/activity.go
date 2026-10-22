package roomsvc

import (
	"errors"
	"time"

	"dyna-agent/internal/subagent"
)

// UsageRollup reads the usage rollup for a room (all rooms when empty) over
// the UTC days of the RFC 3339 bounds, both inclusive.
func (s *Service) UsageRollup(roomID, from, to string) (subagent.UsageRollupSnapshot, error) {
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return subagent.UsageRollupSnapshot{}, errors.New("Invalid usage start time.")
	}
	end, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return subagent.UsageRollupSnapshot{}, errors.New("Invalid usage end time.")
	}
	if end.Before(start) {
		return subagent.UsageRollupSnapshot{}, errors.New("Usage end time precedes start time.")
	}
	return s.sessions().ReadUsageRollup(roomID, start.UTC().Format(time.DateOnly), end.UTC().Format(time.DateOnly))
}
