package watch

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"dyna-agent/internal/goals"
)

// Counters are process-local, shared by worker and status Service instances.
// Reviews grant capacity per room; merge trials grant capacity per repository.
var expediteQueues sync.Map // PR store root -> *expediteCounters

type expediteCounters struct {
	mu       sync.Mutex
	streaks  map[string]int
	revision uint64
}

func (s *Service) expediteCounters() *expediteCounters {
	value, _ := expediteQueues.LoadOrStore(s.Store.Root, &expediteCounters{streaks: map[string]int{}})
	return value.(*expediteCounters)
}

func (s *Service) expediteStreak(queue string) int {
	if s.Store == nil {
		return 0
	}
	c := s.expediteCounters()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streaks[queue]
}

func (s *Service) expediteGranted(queue string, expedited bool) {
	c := s.expediteCounters()
	c.mu.Lock()
	defer c.mu.Unlock()
	if expedited {
		c.streaks[queue] = min(c.streaks[queue]+1, 3)
	} else {
		c.streaks[queue] = 0
	}
	c.revision++
}

func (s *Service) queueGoalStore() *goals.Store {
	return &goals.Store{Root: filepath.Join(filepath.Dir(s.Store.Root), "goals"), Now: s.Now}
}

// PriorityInputs invalidates view caches when a goal or grant changes, and at
// the next safety expiry even if no writer runs at that instant.
func (s *Service) PriorityInputs() (string, time.Time, error) {
	store := s.queueGoalStore()
	revision, err := store.Revision()
	if err != nil {
		return "", time.Time{}, err
	}
	rooms, until, err := s.expeditedGoals()
	if err != nil {
		return "", time.Time{}, err
	}
	c := s.expediteCounters()
	c.mu.Lock()
	revision = fmt.Sprintf("%s/%d/%d", revision, c.revision, len(rooms))
	c.mu.Unlock()
	return revision, until, nil
}

func (s *Service) expeditedGoals() (map[string]string, time.Time, error) {
	if s.Store == nil {
		return map[string]string{}, time.Time{}, nil
	}
	st, err := s.queueGoalStore().ReadOpen()
	if err != nil {
		return nil, time.Time{}, err
	}
	active := map[string]string{}
	var until time.Time
	for room, state := range st.Rooms {
		for _, goal := range state.Goals {
			if goal.ExpediteUntil == nil || !goal.ExpediteUntil.After(s.now()) {
				continue
			}
			active[room] = goal.ID
			if until.IsZero() || goal.ExpediteUntil.Before(until) {
				until = *goal.ExpediteUntil
			}
		}
	}
	return active, until, nil
}

func expeditedRecord(r Record, active map[string]string) bool {
	return r.GoalID != "" && active[r.RoomID] == r.GoalID
}

// nextPriority leaves the established ordering intact within each class.
// The fairness turn selects the oldest normal item, even if its CI is pending.
func nextPriority[T any](items []T, streak int, expedited func(T) bool, older func(T, T) bool) int {
	firstExpedited, oldestNormal := -1, -1
	for i, item := range items {
		if expedited(item) {
			if firstExpedited < 0 {
				firstExpedited = i
			}
		} else if oldestNormal < 0 || older(item, items[oldestNormal]) {
			oldestNormal = i
		}
	}
	if streak >= 3 && oldestNormal >= 0 {
		return oldestNormal
	}
	if firstExpedited >= 0 {
		return firstExpedited
	}
	return 0
}

func reviewPriority(queue []*Record, active map[string]string, streak int) []*Record {
	pending := append([]*Record(nil), queue...)
	out := make([]*Record, 0, len(queue))
	for len(pending) > 0 {
		i := nextReview(pending, active, streak)
		r := pending[i]
		out = append(out, r)
		pending = append(pending[:i], pending[i+1:]...)
		if expeditedRecord(*r, active) {
			streak++
		} else {
			streak = 0
		}
	}
	return out
}

func nextReview(queue []*Record, active map[string]string, streak int) int {
	return nextPriority(queue, streak, func(r *Record) bool { return expeditedRecord(*r, active) }, func(a, b *Record) bool {
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.Key < b.Key
	})
}

// chooseMergeBatch isolates expedited work. A fairness grant starts with the
// oldest normal member and may include other normal members in that batch.
func chooseMergeBatch(queue []Record, active map[string]string, streak int) []Record {
	if len(queue) == 0 {
		return nil
	}
	i := nextPriority(queue, streak, func(r Record) bool { return expeditedRecord(r, active) }, func(a, b Record) bool { return false })
	if expeditedRecord(queue[i], active) {
		return []Record{queue[i]}
	}
	normal := make([]Record, 0, len(queue))
	for _, r := range queue {
		if !expeditedRecord(r, active) {
			normal = append(normal, r)
		}
	}
	return normal
}

func (s *Service) prioritizedMergeQueue(ctx context.Context, st State) ([]MergeQueueItem, error) {
	active, _, err := s.expeditedGoals()
	if err != nil {
		return nil, err
	}
	rows := mergeQueue(st, s.LocalTrialRooms(ctx), s.roomUpstreams())
	indices := map[string][]int{}
	pauses := map[string]bool{}
	for i := range rows {
		r := recordByID(&st, rows[i].ChangeID)
		if r == nil {
			continue
		}
		rows[i].GoalID, rows[i].Expedited = r.GoalID, expeditedRecord(*r, active)
		// Held work remains visible, but never consumes a simulated grant.
		paused, err := s.roomPausedCached(r.RoomID, pauses)
		if err != nil {
			return nil, err
		}
		if paused {
			markPaused(&rows[i])
		} else if why, err := s.trialRoomHold(ctx, *r); err != nil {
			return nil, err
		} else if why != "" {
			if st.MergePauses[r.RoomID].Reason != "" {
				rows[i].State, rows[i].Reason = "merge-paused", why
			} else {
				rows[i].State, rows[i].Reason = "read-only", why
			}
		}
		if rows[i].State == "ready" && rows[i].Trial == nil {
			repo := defaultRepo(*r)
			indices[repo] = append(indices[repo], i)
		}
	}
	for repo, positions := range indices {
		pending := make([]Record, 0, len(positions))
		byKey := map[string]MergeQueueItem{}
		for _, pos := range positions {
			r := *recordByID(&st, rows[pos].ChangeID)
			pending = append(pending, r)
			byKey[r.Key] = rows[pos]
		}
		streak, offset := s.expediteStreak("merge:"+repo), 0
		for len(pending) > 0 {
			batch := chooseMergeBatch(pending, active, streak)
			chosen := map[string]bool{}
			for _, r := range batch {
				rows[positions[offset]] = byKey[r.Key]
				offset++
				chosen[r.Key] = true
			}
			if expeditedRecord(batch[0], active) {
				streak++
			} else {
				streak = 0
			}
			rest := pending[:0]
			for _, r := range pending {
				if !chosen[r.Key] {
					rest = append(rest, r)
				}
			}
			pending = rest
		}
	}
	return rows, nil
}
