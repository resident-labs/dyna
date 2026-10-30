//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// WatchTranscript sends file changes and retains the last acknowledged offset.
func WatchTranscript(ctx context.Context, client TranscriptAppender, path string) error {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "inotify")
	defer file.Close()
	_, err = syscall.InotifyAddWatch(fd, filepath.Dir(path), syscall.IN_CREATE|syscall.IN_MODIFY|syscall.IN_MOVED_TO|syscall.IN_CLOSE_WRITE)
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			file.Close()
		case <-stop:
		}
	}()
	var offset int64
	buf := make([]byte, 4096)
	for {
		next, sendErr := SendTranscript(ctx, client, path, offset)
		offset = next
		if sendErr != nil && !errors.Is(sendErr, os.ErrNotExist) {
			return sendErr
		}
		_, err := file.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return err
		}
	}
}
