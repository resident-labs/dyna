//go:build linux

package shells

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Events reads job change events.
type Events struct {
	fd  int
	buf []byte
}

// WatchEvents starts reading job change events. Start it before a full read
// of the store, so no change after that read is missed.
func (s *Store) WatchEvents() (*Events, error) {
	dir := filepath.Join(s.Root, eventsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_CREATE); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &Events{fd: fd, buf: make([]byte, 64<<10)}, nil
}

// Read returns the jobs that changed since the last call, without waiting.
// An error means events were lost: read every job again, with a new Events.
func (e *Events) Read() (map[string]bool, error) {
	ids := map[string]bool{}
	for {
		n, err := unix.Read(e.fd, e.buf)
		if errors.Is(err, unix.EAGAIN) {
			return ids, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			mask := binary.NativeEndian.Uint32(e.buf[off+4:])
			size := int(binary.NativeEndian.Uint32(e.buf[off+12:]))
			name := e.buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+size]
			off += unix.SizeofInotifyEvent + size
			if mask&(unix.IN_Q_OVERFLOW|unix.IN_IGNORED) != 0 {
				return nil, fmt.Errorf("job change events were lost (inotify mask %#x)", mask)
			}
			if id, ok := eventJob(strings.TrimRight(string(name), "\x00")); ok {
				ids[id] = true
			}
		}
	}
}

func (e *Events) Close() { unix.Close(e.fd) }
