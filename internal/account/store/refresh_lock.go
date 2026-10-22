package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WithAccountLock serializes refresh across processes. Take the registry
// transaction first so every caller uses the same lock order.
func (s *Store) WithAccountLock(ctx context.Context, harness, integration, identity string, fn func() error) error {
	dir := filepath.Join(s.Root, ".refresh-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Account refresh lock directory is unsafe.")
	}
	key := sha256.Sum256([]byte(harness + "\x00" + integration + "\x00" + identity))
	fd, err := syscall.Open(filepath.Join(dir, fmt.Sprintf("%x.lock", key)), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "account-refresh")
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	return fn()
}
