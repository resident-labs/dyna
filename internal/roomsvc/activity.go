package roomsvc

import (
	"errors"
	"time"

	"dyna-agent/internal/subagent"
)

// UsageRollup reads the usage rollup for a room (all rooms when empty) over
// the UTC days of the RFC 3339 bounds, both inclusive.
func (s *Service) UsageRollup(roomID, from, to string) (subagent.UsageRollupSnapshot, error) {
	startDay, endDay, err := usageDays(from, to)
	if err != nil {
		return subagent.UsageRollupSnapshot{}, err
	}
	return s.sessions().ReadUsageRollup(roomID, startDay, endDay)
}

// ProfileUsage reads measured activity for every profile over inclusive UTC days.
func (s *Service) ProfileUsage(from, to string) (subagent.ProfileUsageSnapshot, error) {
	startDay, endDay, err := usageDays(from, to)
	if err != nil {
		return subagent.ProfileUsageSnapshot{}, err
	}
	return s.sessions().ReadProfileUsage(startDay, endDay)
}

func usageDays(from, to string) (string, string, error) {
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return "", "", errors.New("Invalid usage start time.")
	}
	end, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return "", "", errors.New("Invalid usage end time.")
	}
	if end.Before(start) {
		return "", "", errors.New("Usage end time precedes start time.")
	}
	return start.UTC().Format(time.DateOnly), end.UTC().Format(time.DateOnly), nil
}
