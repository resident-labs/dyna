package home

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// publicationQueues holds one mutex per lock path. Goroutines in this process
// wait in turn there, so the non-blocking flock poll below only competes with
// other processes and cannot starve one goroutine past its deadline.
var publicationQueues sync.Map

// lockPublication serializes writers across processes. Never remove the lock
// file: another process may already hold an open descriptor for its inode.
func lockPublication(dir string) (func(), error) {
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "prepare.lock")
	queue, _ := publicationQueues.LoadOrStore(path, &sync.Mutex{})
	mu := queue.(*sync.Mutex)
	mu.Lock()
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(err error) (func(), error) { f.Close(); mu.Unlock(); return nil, err }
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		return fail(fmt.Errorf("unsafe publication lock %q", path))
	}
	if err := f.Chmod(0600); err != nil {
		return fail(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close(); mu.Unlock() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return fail(err)
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("timed out waiting for private HOME publication lock"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
