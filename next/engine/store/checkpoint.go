package store

import (
	"os"
	"time"
)

const checkpointBytes = 64 << 20
const checkpointInterval = 30 * time.Second
const modelStampRetention = 10 * time.Minute

func (s *Store) checkLoop(path string) {
	defer close(s.checkDone)
	last := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	stamps := time.NewTicker(modelStampRetention)
	defer stamps.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-stamps.C:
			s.clearModelStamps()
		case <-ticker.C:
			info, err := os.Stat(path + "-wal")
			if err != nil {
				continue
			}
			if info.Size() >= checkpointBytes || time.Since(last) >= checkpointInterval {
				_ = s.checkpointRun()
				last = time.Now()
			}
		}
	}
}
