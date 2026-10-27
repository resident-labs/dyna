//go:build !linux

package shells

import (
	"time"
)

// DirWatch polls where inotify is not available.
type DirWatch struct{}

func WatchDir(string) (*DirWatch, error) { return &DirWatch{}, nil }

func (*DirWatch) Next(d time.Duration) error {
	time.Sleep(min(d, 100*time.Millisecond))
	return nil
}

func (*DirWatch) Wake() {}

func (*DirWatch) Close() {}
