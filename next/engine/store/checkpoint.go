package store

import (
	"os"
	"time"
)

const checkpointBytes = 64 << 20
const checkpointInterval = 30 * time.Second

// checkpoint runs independently from the writer. The callback is injectable
// so a unit test can hold a checkpoint open while a write completes.
func (s *Store) checkpointOnce() error { return s.checkpointRun() }

func (s *Store) checkLoop(path string, now func() time.Time) {
	defer close(s.checkDone)
	if now == nil {
		now = time.Now
	}
	last := now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			info, err := os.Stat(path + "-wal")
			if err != nil {
				continue
			}
			if info.Size() >= checkpointBytes || now().Sub(last) >= checkpointInterval {
				_ = s.checkpointOnce()
				last = now()
			}
		}
	}
}
