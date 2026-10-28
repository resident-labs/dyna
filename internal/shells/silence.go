package shells

import (
	"os"
	"time"
)

// Silent lists running jobs whose output log has not grown for the interval.
// Capped logs cannot show further growth and are excluded.
func (s *Store) Silent(now time.Time, interval time.Duration) ([]Info, error) {
	if interval <= 0 {
		return nil, nil
	}
	jobs, err := s.List(false)
	if err != nil {
		return nil, err
	}
	var silent []Info
	for _, job := range jobs {
		if job.Status != "running" || job.RoomID == "" {
			continue
		}
		path, err := s.OutputPath(job.ID)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err == nil {
			modified := info.ModTime()
			if job.Started.After(modified) {
				modified = job.Started
			}
			if silentLog(now, modified, info.Size(), interval) {
				silent = append(silent, job)
			}
		}
	}
	return silent, nil
}

func silentLog(now, modified time.Time, size int64, interval time.Duration) bool {
	return interval > 0 && size < MaxOutputBytes && !modified.After(now.Add(-interval))
}
