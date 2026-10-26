// Package admission limits how many agent turns run at once on this machine.
// A turn takes a slot before its harness starts and keeps it until the turn
// ends. A slot is a file lock, so the kernel frees it when the turn's process
// dies and no stale slot needs cleaning. The limit follows the machine's memory
// and CPU pressure, so it fits any owner's hardware.
//
// The gate is built never to be the reason a turn fails: every lock wait is
// bounded, a turn that has waited its longest runs without a slot, and the
// caller treats any gate error other than cancellation as permission to run.
package admission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Pool is a set of slots. Child sessions have their own pool, so a parent that
// waits on its children never waits behind them, and the children never wait
// behind their parents.
type Pool string

// The pools.
const (
	Turns    Pool = "turns"
	Children Pool = "children"
)

// maxSlots is the most slot files a pool holds. Past it a turn that must run
// (it waited its longest) runs without a file.
const maxSlots = 256

// DefaultLockTimeout bounds the wait for the allocation lock.
const DefaultLockTimeout = 3 * time.Second

// ErrLockTimeout means another process held the allocation lock too long. The
// caller lets the turn run.
var ErrLockTimeout = errors.New("the admission lock stayed busy")

// Reason says why a turn is held.
type Reason string

// The reasons.
const (
	// ReasonLimit: the pool already runs as many turns as its limit allows.
	ReasonLimit Reason = "limit"
	// ReasonRoomShare: the turn's own room holds its whole share of the pool.
	ReasonRoomShare Reason = "room-share"
)

// Hold describes why a turn waits, for the record the session view shows.
type Hold struct {
	Pool   Pool
	Reason Reason
	Limit  int
	Held   int
	// Forced is true when the turn waited its longest and was let in.
	Forced bool
}

// Slot is one held slot: the room that holds it and the runner process.
type Slot struct {
	Room string
	PID  int
}

// Gate hands out slots under Dir.
type Gate struct {
	Dir string
	// Limit returns the pool's limit given the slots held now. A negative
	// limit means the pool is not gated. It runs under the allocation lock, so
	// it may read and write state that the gate's other callers share.
	Limit func(pool Pool, slots []Slot) int
	// RoomShare is the most slots one room may hold of a pool with this limit.
	// It keeps one busy room from taking the whole machine.
	RoomShare func(limit int) int
	// Bypass is asked at every attempt; a true answer admits the turn at once,
	// without a slot. It carries facts that can change while a turn waits.
	Bypass func() bool
	// MaxWait admits a turn that has waited this long, whatever the limit. Zero
	// means no bound. Now is the clock it reads.
	MaxWait time.Duration
	Now     func() time.Time
	// LockTimeout bounds the wait for the allocation lock; zero uses
	// DefaultLockTimeout.
	LockTimeout time.Duration
	// Wait blocks until the next attempt, or returns the context error.
	Wait func(ctx context.Context) error
}

// Acquire waits for a slot in pool for a turn of room. onHold runs when the
// first attempt fails, and again if the turn is let in after its longest wait.
// The returned release frees the slot; calling it twice is safe. An error other
// than the context's means the gate itself failed.
func (g Gate) Acquire(ctx context.Context, pool Pool, room string, onHold func(Hold)) (func(), error) {
	dir := filepath.Join(g.Dir, string(pool))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	held := false
	var since time.Time
	if g.MaxWait > 0 && g.Now != nil {
		since = g.Now()
	}
	for {
		if g.Bypass != nil && g.Bypass() {
			return func() {}, nil
		}
		force := !since.IsZero() && g.Now().Sub(since) >= g.MaxWait
		release, hold, err := g.attempt(ctx, dir, pool, room, force)
		if err != nil {
			return nil, err
		}
		if release != nil {
			if hold.Forced && onHold != nil {
				onHold(hold)
			}
			return release, nil
		}
		if !held {
			held = true
			if onHold != nil {
				onHold(hold)
			}
		}
		if err := g.Wait(ctx); err != nil {
			return nil, err
		}
	}
}

// attempt tries once to take a slot. The scan, the limit check, the room check
// and the lock of a slot run under one allocation lock, so two runners cannot
// both see room for themselves. A nil release means the turn must wait. A turn
// that is forced always gets a release, with a slot when one is free.
func (g Gate) attempt(ctx context.Context, dir string, pool Pool, room string, force bool) (func(), Hold, error) {
	unlock, err := g.lockAlloc(ctx, dir)
	if err != nil {
		return nil, Hold{}, err
	}
	defer unlock()
	slots, mine := scan(dir, room)
	limit := g.Limit(pool, slots)
	if limit < 0 {
		return func() {}, Hold{}, nil
	}
	limit = min(limit, maxSlots)
	share := limit
	if g.RoomShare != nil {
		share = g.RoomShare(limit)
	}
	var hold Hold
	switch {
	case len(slots) >= limit:
		hold = Hold{Pool: pool, Reason: ReasonLimit, Limit: limit, Held: len(slots)}
	case mine >= share:
		hold = Hold{Pool: pool, Reason: ReasonRoomShare, Limit: limit, Held: len(slots)}
	}
	held := hold.Reason != ""
	if held && !force {
		return nil, hold, nil
	}
	release, ok, err := take(dir, room)
	if err != nil {
		if force {
			return func() {}, withForced(hold, held), nil
		}
		return nil, Hold{}, err
	}
	if !ok {
		if force {
			// Every slot file is locked: run without one rather than wait.
			return func() {}, withForced(hold, held), nil
		}
		return nil, Hold{Pool: pool, Reason: ReasonLimit, Limit: limit, Held: len(slots)}, nil
	}
	return release, withForced(hold, held), nil
}

func withForced(h Hold, held bool) Hold {
	h.Forced = held
	return h
}

// lockAlloc takes the allocation lock without blocking forever: it retries
// until LockTimeout, with Wait between tries, and returns ErrLockTimeout after.
func (g Gate) lockAlloc(ctx context.Context, dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".alloc"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	timeout := g.LockTimeout
	if timeout <= 0 {
		timeout = DefaultLockTimeout
	}
	var start time.Time
	if g.Now != nil {
		start = g.Now()
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if g.Now == nil || g.Now().Sub(start) >= timeout {
			f.Close()
			return nil, ErrLockTimeout
		}
		if err := g.Wait(ctx); err != nil {
			f.Close()
			return nil, err
		}
	}
}

func slotPath(dir string, i int) string { return filepath.Join(dir, fmt.Sprintf("slot-%03d", i)) }

// take locks the first free slot and writes the room and process into it.
func take(dir string, room string) (func(), bool, error) {
	for i := 0; i < maxSlots; i++ {
		f, err := os.OpenFile(slotPath(dir, i), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, false, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				continue
			}
			return nil, false, err
		}
		if err := f.Truncate(0); err == nil {
			_, _ = f.WriteString(room + "\n" + strconv.Itoa(os.Getpid()))
		}
		released := false
		return func() {
			if released {
				return
			}
			released = true
			_ = f.Truncate(0)
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}, true, nil
	}
	return nil, false, nil
}

// scan lists the locked slots and counts those that room holds. A slot is
// locked when a non-blocking lock on a second descriptor fails.
func scan(dir, room string) (slots []Slot, mine int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "slot-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
			continue
		}
		f.Close()
		data, _ := os.ReadFile(path)
		owner, pid, _ := strings.Cut(string(data), "\n")
		n, _ := strconv.Atoi(strings.TrimSpace(pid))
		slots = append(slots, Slot{Room: owner, PID: n})
		if owner == room {
			mine++
		}
	}
	return slots, mine
}
