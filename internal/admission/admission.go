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
	"strings"
	"syscall"
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
}

// Gate hands out slots under Dir.
type Gate struct {
	Dir string
	// Limit returns the pool's limit given the slots held now. A negative
	// limit means the pool is not gated.
	Limit func(pool Pool, held int) int
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
	for {
		release, hold, err := g.attempt(dir, pool, room)
		if err != nil {
			return nil, err
		}
		if release != nil {
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
func (g Gate) attempt(dir string, pool Pool, room string) (func(), Hold, error) {
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
	limit := g.Limit(pool, len(slots))
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
	if len(slots) >= limit {
		return nil, Hold{Pool: pool, Limit: limit, Held: len(slots)}, nil
	}
	if mine >= share {
		return nil, Hold{Pool: pool, Limit: limit, Held: len(slots), RoomFull: true}, nil
	}
	release, ok, err := take(dir, room)
	if err != nil {
		return nil, Hold{}, err
	}
	if !ok {
		return nil, Hold{Pool: pool, Limit: limit, Held: len(slots)}, nil
	}
	return release, Hold{}, nil
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
			_, _ = f.WriteString(room)
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
func scan(dir, room string) (rooms []string, mine int) {
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
		owner, _ := os.ReadFile(path)
		rooms = append(rooms, string(owner))
		if string(owner) == room {
			mine++
		}
	}
	return rooms, mine
}
