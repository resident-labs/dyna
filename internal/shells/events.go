package shells

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A job change event is an empty file named <job id>.<random>, created and
// removed at once in <root>/events. A watcher reads the names with inotify,
// so it learns which job changed without reading the store or any job
// folder. Every write to a job row sends one, and so does a supervisor while
// its output grows.
const eventsDir = "events"

// Notify tells watchers that job id changed. It is best effort: a lost event
// only delays a watcher's view of the job until its next change.
func (s *Store) Notify(id string) {
	dir := filepath.Join(s.Root, eventsDir)
	var b [4]byte
	_, _ = rand.Read(b[:])
	path := filepath.Join(dir, id+"."+hex.EncodeToString(b[:]))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) && os.MkdirAll(dir, 0o700) == nil {
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return
	}
	_ = f.Close()
	_ = os.Remove(path)
}

// eventJob returns the job ID in an event file name.
func eventJob(name string) (string, bool) {
	id, _, ok := strings.Cut(name, ".")
	return id, ok && ValidID(id)
}

// outputEventEvery is the shortest time between two output events of one job.
var outputEventEvery = time.Second

// outputEvents sends a job's output events: one at once, then at most one per
// outputEventEvery, the last one after the last output.
type outputEvents struct {
	mu      sync.Mutex
	send    func()
	last    time.Time
	timer   *time.Timer
	stopped bool
	// inflight counts a callback that is running send, so a stop can wait for
	// it instead of returning while the store is still being touched.
	inflight sync.WaitGroup
}

// grew reports new output.
func (o *outputEvents) grew() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.timer != nil || o.stopped {
		return
	}
	o.timer = time.AfterFunc(time.Until(o.last.Add(outputEventEvery)), func() {
		o.mu.Lock()
		o.timer, o.last = nil, time.Now()
		stopped := o.stopped
		if !stopped {
			o.inflight.Add(1)
		}
		o.mu.Unlock()
		if stopped {
			return
		}
		defer o.inflight.Done()
		o.send()
	})
}

// stop drops a pending event and waits for one that already began, so no
// event reaches the store after the caller returns. The exit's own write
// sends the last one.
func (o *outputEvents) stop() {
	o.mu.Lock()
	o.stopped = true
	if o.timer != nil {
		o.timer.Stop()
	}
	o.mu.Unlock()
	o.inflight.Wait()
}
