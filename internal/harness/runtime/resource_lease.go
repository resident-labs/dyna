package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/store"
)

// ResourceLease stays in the Dyna runner. Native children receive no lock files.
// A bound process reference protects the resource during orphan cleanup too.
type ResourceLease struct {
	mu         sync.Mutex
	path       string
	file       *os.File
	record     resourceUse
	closed     bool
	afterClose func()
}
type resourceUse struct {
	Version   int             `json:"version"`
	PID       int             `json:"pid"`
	Released  bool            `json:"released,omitempty"`
	Reference json.RawMessage `json:"reference,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// The process guard bridge replaces these fail-closed hooks. Tests use fake
// references without starting native harnesses or contacting providers.
var nativeUseProbe = func(json.RawMessage) (bool, error) {
	return true, errors.New("Native process liveness is unavailable.")
}
var nativeUseSignal = func(json.RawMessage, syscall.Signal) error {
	return errors.New("Native process control is unavailable.")
}

func newResourceLease(ctx context.Context, root, prefix string, data any) (*ResourceLease, error) {
	if err := safeAccountPath(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	path := filepath.Join(root, prefix+hex.EncodeToString(nonce[:])+".lease")
	f, err := acquireAccountLock(ctx, path, syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		releaseAccountLock(f)
		_ = os.Remove(path)
		return nil, err
	}
	l := &ResourceLease{path: path, file: f, record: resourceUse{Version: 1, PID: os.Getpid(), Data: raw}}
	if err = store.WriteJSON(path+".json", l.record, 0600); err != nil {
		releaseAccountLock(f)
		_ = os.Remove(path)
		return nil, err
	}
	return l, nil
}
func (l *ResourceLease) Bind(reference json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(reference) == 0 || !json.Valid(reference) {
		return errors.New("Native process protection could not be bound.")
	}
	l.record.Reference = append(json.RawMessage(nil), reference...)
	return store.WriteJSON(l.path+".json", l.record, 0600)
}
func (l *ResourceLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.record.Released = true
	err := store.WriteJSON(l.path+".json", l.record, 0600)
	releaseAccountLock(l.file)
	if l.afterClose != nil {
		l.afterClose()
	}
	if err != nil {
		return err
	}
	live, _, probeErr := resourceLeaseState(l.path)
	if !live && probeErr == nil {
		removeResourceLease(l.path)
	}
	return probeErr
}
func removeResourceLease(path string) { _ = os.Remove(path + ".json"); _ = os.Remove(path) }

func resourceLeaseState(path string) (bool, resourceUse, error) {
	var record resourceUse
	f, err := openAccountLock(path, false)
	if os.IsNotExist(err) {
		return false, record, nil
	}
	if err != nil {
		return true, record, err
	}
	defer f.Close()
	lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	busy := errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN)
	if lockErr != nil && !busy {
		return true, record, lockErr
	}
	if lockErr == nil {
		defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	if err = safeAccountPath(path + ".json"); err != nil {
		return true, record, err
	}
	raw, err := os.ReadFile(path + ".json")
	if os.IsNotExist(err) {
		return busy, record, nil
	} // Pre-safety diagnostic record.
	if err != nil || len(raw) > 64<<10 || json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.PID <= 0 {
		return true, record, errors.New("Native process protection could not be read safely.")
	}
	if busy {
		return true, record, nil
	}
	if len(record.Reference) > 0 {
		live, err := nativeUseProbe(record.Reference)
		return live || err != nil, record, err
	}
	if !record.Released && syscall.Kill(record.PID, 0) != syscall.ESRCH {
		return true, record, nil
	}
	return false, record, nil
}
func resourceLeasePaths(root, prefix string) ([]string, error) {
	if err := safeAccountPath(root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".lease") {
			paths = append(paths, filepath.Join(root, e.Name()))
		}
	}
	return paths, nil
}
func resourceLeaseCount(root string) (int, error) {
	paths, err := resourceLeasePaths(root, "")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, path := range paths {
		live, _, e := resourceLeaseState(path)
		if live {
			count++
		} else if e == nil {
			removeResourceLease(path)
		}
		if e != nil {
			return count, e
		}
	}
	return count, nil
}

// NativeProcessAlive probes the guarded native group without exposing its
// private control capability to desktop clients or native children.
func NativeProcessAlive(reference json.RawMessage) (bool, error) {
	return nativeUseProbe(reference)
}

// drainResourceLeases includes native children whose runner has already exited.
func drainResourceLeases(ctx context.Context, root string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		count, err := resourceLeaseCount(root)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
