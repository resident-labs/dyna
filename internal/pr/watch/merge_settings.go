package watch

import (
	"dyna-agent/internal/room"
	"fmt"
)

type MergeSettings struct {
	MaxParallel int `json:"maxParallel"`
}

func (s *Service) MergeSettings(roomID string) (MergeSettings, error) {
	r, err := s.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return MergeSettings{}, err
	}
	n := r.MergeMaxParallel
	if n <= 0 {
		n = 3
	}
	return MergeSettings{MaxParallel: n}, nil
}
func (s *Service) SetMergeMaxParallel(roomID string, n int) error {
	if n < 1 {
		return fmt.Errorf("merge max-parallel must be a positive integer")
	}
	return s.Notify.Rooms.UpdatePreferences(roomID, func(r *room.Record) error { r.MergeMaxParallel = n; return nil })
}
