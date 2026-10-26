// Package admission limits how many agent turns run at once on this machine.
// A turn takes a slot before its harness starts and keeps it until the turn
// ends. A slot is a file lock, so the kernel frees it when the turn's process
// dies and no stale slot needs cleaning. The limit follows the machine's free
// memory and load, so it fits any owner's hardware.
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

// Pool is a set of slots. Leaf sessions have their own pool, so a parent that
// waits on its children never waits behind them, and the children never wait
// behind their parents.
type Pool string

// The pools.
const (
	Turns  Pool = "turns"
	Leaves Pool = "leaves"
)

// maxSlots is the most slot files a pool can hold. A limit above it is cut.
const maxSlots = 256

// Hold describes why a turn waits, for the notice the room gets.
type Hold struct {
	Pool  Pool
	Limit int
	Held  int
	// RoomFull is true when the room itself holds its whole share while other
	// slots are free.
	RoomFull bool
	// Observed is true when the gate is only watching: the turn was let in.
	Observed bool
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
	// Observe lets every turn in and only reports, through onHold, the turns
	// that enforcing would have held.
	Observe bool
	// MaxWait admits a turn that has waited this long, whatever the limit. Zero
	// means no bound. Now is the clock it reads.
	MaxWait time.Duration
	Now     func() time.Time
	// RoomShare is the most slots one room may hold of a pool with this limit.
	// It keeps one busy room from taking the whole machine.
	RoomShare func(limit int) int
	// Wait blocks until the next attempt, or returns the context error.
	Wait func(ctx context.Context) error
}

// Acquire waits for a slot in pool for a turn of room. onHold runs once, when
// the first attempt fails. The returned release frees the slot; calling it
// twice is safe.
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
		force := !since.IsZero() && g.Now().Sub(since) >= g.MaxWait
		release, hold, err := g.attempt(dir, pool, room, force)
		if err != nil {
			return nil, err
		}
		if release != nil {
			if (hold.Observed || hold.Forced) && onHold != nil {
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
// both see room for themselves. A nil release means the turn must wait.
func (g Gate) attempt(dir string, pool Pool, room string, force bool) (func(), Hold, error) {
	alloc, err := os.OpenFile(filepath.Join(dir, ".alloc"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, Hold{}, err
	}
	defer alloc.Close()
	if err := syscall.Flock(int(alloc.Fd()), syscall.LOCK_EX); err != nil {
		return nil, Hold{}, err
	}
	defer syscall.Flock(int(alloc.Fd()), syscall.LOCK_UN)
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
	// The count of live slots, not a slot index, is the limit: a limit that
	// falls below the slots in use admits nobody until enough of them end.
	var hold Hold
	switch {
	case len(slots) >= limit:
		hold = Hold{Pool: pool, Limit: limit, Held: len(slots)}
	case mine >= share:
		hold = Hold{Pool: pool, Limit: limit, Held: len(slots), RoomFull: true}
	}
	if hold.Limit != 0 && !g.Observe && !force {
		return nil, hold, nil
	}
	release, ok, err := take(dir, room)
	if err != nil {
		return nil, Hold{}, err
	}
	if !ok {
		return nil, Hold{Pool: pool, Limit: limit, Held: len(slots)}, nil
	}
	hold.Observed, hold.Forced = g.Observe && hold.Limit != 0, force && hold.Limit != 0
	return release, hold, nil
}

func slotPath(dir string, i int) string { return filepath.Join(dir, fmt.Sprintf("slot-%03d", i)) }

// take locks the first free slot and writes the room into it.
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
