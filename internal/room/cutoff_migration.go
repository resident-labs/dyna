package room

import "fmt"

// TODO: legacy/shim remove later — converts retained turn end times to room-log cutoffs; remove with subagent.turns.cutoff.
func (s *Store) OffsetAtTime(roomID string, ts int64) (int64, error) {
	var offset int64
	var crossed bool
	var orderErr error
	err := s.withMessageLog(roomID, func(log *messageLog) {
		offset = log.offset
		for _, line := range log.lines {
			if line.msg.ID == "" {
				continue
			}
			if line.msg.TS > ts {
				if !crossed {
					offset = line.start
					crossed = true
				}
			} else if crossed {
				orderErr = fmt.Errorf("room %s timestamps cross the cutoff more than once", roomID)
				return
			}
		}
	})
	if err != nil {
		return 0, err
	}
	return offset, orderErr
}
