package engine

import "time"

// inputBatchTimer is a single stream-owned scheduled kick. Tests replace the
// clock and this timer together; production never polls a pending delivery.
func (s *Service) inputBatchTimer(delay time.Duration) (<-chan time.Time, func()) {
	if s.batchTimer != nil {
		return s.batchTimer(delay)
	}
	timer := time.NewTimer(delay)
	return timer.C, func() { timer.Stop() }
}
