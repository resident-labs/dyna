//go:build !linux

package shells

import "time"

// dirWatch polls where inotify is not available.
type dirWatch struct{}

func watchDir(string) (*dirWatch, error) { return &dirWatch{}, nil }

func (*dirWatch) next(d time.Duration) error {
	time.Sleep(min(d, 100*time.Millisecond))
	return nil
}

func (*dirWatch) wake() {}

func (*dirWatch) close() {}
