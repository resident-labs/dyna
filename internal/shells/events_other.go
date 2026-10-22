//go:build !linux

package shells

import "errors"

// Events reads job change events, which need inotify.
type Events struct{}

// WatchEvents fails where inotify is not available; a watcher then reads
// every job on each scan.
func (s *Store) WatchEvents() (*Events, error) {
	return nil, errors.New("job change events need Linux inotify")
}

func (*Events) Read() (map[string]bool, error) { return nil, errors.New("no job change events") }

func (*Events) Close() {}
