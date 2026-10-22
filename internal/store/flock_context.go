package store

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

type flockContentionKey struct{}

// WithFlockContentionHook lets tests observe the first contended attempt in
// each FlockContext call. The hook runs synchronously with the lock file path.
// It belongs only to the returned context; no global hook needs restoration.
func WithFlockContentionHook(ctx context.Context, onContended func(string)) context.Context {
	return context.WithValue(ctx, flockContentionKey{}, onContended)
}

// FlockContext waits without trapping a cancelled caller behind another process.
// The caller owns the file and must unlock/close it after a successful return.
func FlockContext(ctx context.Context, file *os.File) error {
	if ctx.Done() == nil {
		return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
	}
	delay := 2 * time.Millisecond
	contended := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				return err
			}
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if !contended && (errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
			contended = true
			if onContended, ok := ctx.Value(flockContentionKey{}).(func(string)); ok && onContended != nil {
				onContended(file.Name())
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 50*time.Millisecond)
	}
}
