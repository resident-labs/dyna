// Package lockrank checks the order of nested file locks.
//
// A goroutine may block on a lock only when its rank is not lower than every
// rank it already holds: names, then sessions, then a session's gate, then the
// room root, then a room. The check enforces ranks only: it does not check that
// sessions are taken in ID order, or catch a lock taken twice. An inverted order can deadlock
// silently across processes, so tests and debug builds (DYNA_LOCKCHECK=1)
// panic on it. Other builds skip the check. Non-blocking try-locks cannot
// deadlock and do not call Acquire.
package lockrank

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

type Rank int

const (
	Names Rank = iota + 1
	Session
	Gate
	RoomRoot
	Room
)

var names = map[Rank]string{Names: "names", Session: "session", Gate: "gate", RoomRoot: "room root", Room: "room"}

func (r Rank) String() string { return names[r] }

var (
	enabled = testing.Testing() || os.Getenv("DYNA_LOCKCHECK") == "1"
	mu      sync.Mutex
	held    = map[uint64][]Rank{}
)

// Acquire records that this goroutine takes a lock of rank r, and panics when
// it already holds a lock of a higher rank. Call it before blocking on the
// lock, and call release after unlocking.
func Acquire(r Rank) (release func()) {
	if !enabled {
		return func() {}
	}
	id := goroutine()
	mu.Lock()
	for _, h := range held[id] {
		if r < h {
			mu.Unlock()
			panic(fmt.Sprintf("lockrank: %s lock taken while holding a %s lock; take %s locks first", r, h, r))
		}
	}
	held[id] = append(held[id], r)
	mu.Unlock()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		ranks := held[id]
		for i := len(ranks) - 1; i >= 0; i-- {
			if ranks[i] == r {
				ranks = append(ranks[:i], ranks[i+1:]...)
				break
			}
		}
		if len(ranks) == 0 {
			delete(held, id)
		} else {
			held[id] = ranks
		}
	}
}

// goroutine returns the current goroutine's ID from its stack header.
func goroutine() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseUint(string(b), 10, 64)
	return id
}
