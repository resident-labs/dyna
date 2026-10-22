package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store"
)

func (s Service) spoolDir() string {
	root := s.DataDir
	if root == "" {
		root = store.DataDir()
	}
	return filepath.Join(root, "hook-spool")
}

// Enqueue persists an event without running hooks. A writer lock and sequence
// names preserve append order, including concurrent writers in other processes.
func (s Service) Enqueue(e Event) error {
	e.Event = NormalizeEvent(e.Event)
	source := e.Worktree
	if source == "" {
		source = e.Repo
	}
	r, err := repoid.Resolve(context.Background(), source)
	if err != nil {
		return err
	}
	e.Repo = r.Main
	if e.Worktree == "" {
		e.Worktree = r.Root
	}
	// Capture only private-tree inputs here. Main-checkout hooks sample their
	// current Git values when they run, as they did before deferred preparation.
	if !e.Snapshot && needsSnapshot(e) {
		if e.SHA == "" {
			e.SHA, err = repoid.Git(context.Background(), e.Worktree, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
		} else if !validCommitSHA(e.SHA) {
			e.SHA, err = repoid.Git(context.Background(), e.Worktree, "rev-parse", "--verify", "--end-of-options", e.SHA+"^{commit}")
			if err != nil {
				return err
			}
		}
		if e.Branch == "" {
			e.Branch, _ = repoid.Git(context.Background(), e.Worktree, "symbolic-ref", "--short", "-q", "HEAD")
		}
	}
	dir := s.spoolDir()
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	unlock, err := store.Lock(filepath.Join(dir, "write.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := spoolFiles(dir)
	if err != nil {
		return err
	}
	var next uint64 = 1
	for _, entry := range entries {
		if entry.sequence >= next {
			next = entry.sequence + 1
		}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err = fsutil.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d.json", next)), data, 0600, true); err != nil {
		return err
	}
	if err := fsutil.SyncDir(dir); err != nil {
		return err
	}
	return fsutil.SyncDir(filepath.Dir(dir))
}

type spoolFile struct {
	name     string
	sequence uint64
}

// spoolFiles validates names while the caller holds write.lock. Drain releases
// that short lock before running hooks, so emitters never wait for a command.
func spoolFiles(dir string) ([]spoolFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []spoolFile
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		sequence, err := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if err != nil {
			if err := quarantineSpoolFile(dir, entry.Name(), err); err != nil {
				return nil, err
			}
			continue
		}
		files = append(files, spoolFile{name: entry.Name(), sequence: sequence})
	}
	return files, nil
}

func quarantineSpoolFile(dir, name string, cause error) error {
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".bad")); err != nil {
		return err
	}
	if err := fsutil.SyncDir(dir); err != nil {
		return err
	}
	log.Printf("hook spool: quarantined %q: %v", name, cause)
	return nil
}

// drainRetry is how soon a drain that could not finish runs again.
const drainRetry = time.Minute

// Drain runs each queued event through Dispatch, then removes it, and returns
// the spool files it ran. Hook failures are reported and consumed. A drain
// that cannot finish (another process drains, or a file error stops it)
// returns when to try again, counted from now; otherwise retry is zero.
// Cancellation leaves the event for the next engine. A crash after Dispatch
// but before removal can run the event again.
func (s Service) Drain(ctx context.Context, now time.Time) (ran []string, retry time.Time, err error) {
	retry = now.Add(drainRetry)
	dir := s.spoolDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, retry, err
	}
	unlock, owned, err := store.TryLock(filepath.Join(dir, "drain.lock"))
	if err != nil || !owned {
		return nil, retry, err
	}
	defer unlock()
	unlockWrites, err := store.Lock(filepath.Join(dir, "write.lock"))
	if err != nil {
		return nil, retry, err
	}
	entries, err := spoolFiles(dir)
	unlockWrites()
	if err != nil {
		return nil, retry, err
	}
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return ran, time.Time{}, errors.Join(append(errs, err)...)
		}
		path := filepath.Join(dir, entry.name)
		data, err := os.ReadFile(path)
		if err != nil {
			return ran, retry, errors.Join(append(errs, err)...)
		}
		var e Event
		if err := json.Unmarshal(data, &e); err != nil {
			if err := quarantineSpoolFile(dir, entry.name, err); err != nil {
				return ran, retry, errors.Join(append(errs, err)...)
			}
			continue
		}
		errs = append(errs, s.dispatchQueued(ctx, e))
		if err := ctx.Err(); err != nil {
			return ran, time.Time{}, errors.Join(append(errs, err)...)
		}
		if err := os.Remove(path); err != nil {
			return ran, retry, errors.Join(append(errs, err)...)
		}
		if err := fsutil.SyncDir(dir); err != nil {
			return ran, retry, errors.Join(append(errs, err)...)
		}
		ran = append(ran, entry.name)
		if e.Snapshot {
			cleanupSnapshot(e)
		}
	}
	return ran, time.Time{}, errors.Join(errs...)
}

// needsSnapshot uses the permanent main checkout that Enqueue stores in Repo.
func needsSnapshot(e Event) bool {
	return NormalizeEvent(e.Event) == BeforeLand || e.Worktree != e.Repo
}

// dispatchQueued creates a captured commit's execution tree on the worker.
func (s Service) dispatchQueued(ctx context.Context, e Event) error {
	// TODO: legacy/shim remove later — drains inbox events whose previous-release producer already created their snapshot; remove with item 31.
	if e.Snapshot {
		return s.Dispatch(ctx, e)
	}
	if !needsSnapshot(e) {
		return s.Dispatch(ctx, e)
	}
	prepared, err := snapshot(ctx, e)
	if err == nil {
		defer cleanupSnapshot(prepared)
	} else {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fmt.Errorf("prepare queued hook snapshot at %q: %w", e.SHA, err)
	}
	return s.dispatch(ctx, prepared, err)
}

// SpoolVersion changes when files enter or leave the spool. The store watcher
// uses this cheap directory stat to wake the engine without scanning each event.
func (s Service) SpoolVersion() string {
	info, err := os.Stat(s.spoolDir())
	if err != nil {
		return ""
	}
	return info.ModTime().String()
}
