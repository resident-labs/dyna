package view

import (
	"context"
	"slices"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
)

// MergeControl applies the existing merge controls for the authenticated caller.
func (s *Service) MergeControl(ctx context.Context, roomRef, action string, args, without, flaky []string) error {
	rm, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	actor, err := authority.ActorIn(ctx, rm.ID)
	if err != nil {
		return err
	}
	return s.Watcher().MergeControl(authority.WithActor(ctx, actor), rm.ID, action, args, without, flaky)
}

// SetMergePaused uses the same room and caller checks as the CLI merge controls.
func (s *Service) SetMergePaused(ctx context.Context, roomRef string, paused bool) error {
	action := "resume"
	if paused {
		action = "pause"
	}
	return s.MergeControl(ctx, roomRef, action, nil, nil, nil)
}

// MergeControlStatus includes the same in-memory fairness order as the worker.
func (s *Service) MergeControlStatus(roomRef string) (watch.MergeControlStatus, error) {
	rm, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return watch.MergeControlStatus{}, err
	}
	return s.Watcher().MergeControlStatus(rm.ID)
}

// MergeStatus preserves the full CLI queue response and the engine counters.
func (s *Service) MergeStatus(ctx context.Context) ([]watch.MergeQueueItem, error) {
	return s.Watcher().MergeStatus(ctx)
}

// LocalCIResultRooms finds rooms whose visible PR or merge trial uses a saved
// CI result. It reads only PR state; it does not inspect git or fetch checks.
func (s *Service) LocalCIResultRooms(head string) ([]string, error) {
	if head == "" {
		return nil, nil
	}
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return nil, err
	}
	rooms := map[string]bool{}
	for _, record := range st.Records {
		if record.Head == head {
			rooms[record.RoomID] = true
		}
	}
	for _, trial := range st.MergeTrials {
		if trial == nil {
			continue
		}
		usesHead := trial.Head == head
		for _, stack := range trial.Stacks {
			usesHead = usesHead || stack.Head == head
		}
		if !usesHead {
			continue
		}
		for _, change := range trial.Changes {
			if record := st.Records[change.Key]; record != nil {
				rooms[record.RoomID] = true
			}
		}
	}
	delete(rooms, "")
	result := make([]string, 0, len(rooms))
	for roomID := range rooms {
		result = append(result, roomID)
	}
	slices.Sort(result)
	return result, nil
}
