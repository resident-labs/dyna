package shells

import (
	"time"

	"dyna-agent/internal/activity"
)

// QuietJob has a running shell and the output timestamp that identifies its quiet period.
type QuietJob struct {
	Info
	LastOutput time.Time
}

// Silent lists shells with continuous, fresh output observation for the interval.
func (s *Store) Silent(now time.Time, interval time.Duration) ([]QuietJob, error) {
	if interval <= 0 {
		return nil, nil
	}
	jobs, err := s.List(false)
	if err != nil {
		return nil, err
	}
	var silent []QuietJob
	for _, job := range jobs {
		if job.Status != "running" || job.RoomID == "" {
			continue
		}
		signal, err := s.Activity(job.ID, now)
		if err != nil || !quietPeriod(signal, now, interval) {
			continue
		}
		silent = append(silent, QuietJob{Info: job, LastOutput: signal.LastOutput})
	}
	return silent, nil
}

func quietPeriod(s activity.Signal, now time.Time, interval time.Duration) bool {
	s = s.At(now)
	if interval <= 0 || s.Copier != activity.CopierRunning || s.ObservedSince.IsZero() || s.ObservedSince.After(now) || s.LastOutput.After(now) {
		return false
	}
	quietSince := s.ObservedSince
	if s.LastOutput.After(quietSince) {
		quietSince = s.LastOutput
	}
	// The lease is based on the copier's last proof. Output after that proof
	// may still be waiting for publication, so do not count that time.
	proof := s.LeaseUntil.Add(-activity.Lease)
	if proof.After(now) {
		return false
	}
	return !quietSince.After(proof.Add(-interval))
}
