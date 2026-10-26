package admission

import (
	"context"
	"sync/atomic"
	"testing"
)

const gib = 1 << 30

func TestLimitFollowsMemoryAndOverload(t *testing.T) {
	machine := Readings{Cores: 8, Load1: 1, MemTotal: 32 * gib, MemAvail: 20 * gib}
	// Reserve is a tenth of 32 GiB; the other free memory takes one turn per GiB.
	if got, want := Limit(Policy{}, machine, 3), 3+int((20*gib-32*gib/10)/gib); got != want {
		t.Errorf("memory-bound limit = %d, want %d", got, want)
	}
	tight := machine
	tight.MemAvail = gib
	if got := Limit(Policy{}, tight, 0); got != 1 {
		t.Errorf("a full machine must still run one turn, got %d", got)
	}
	busy := machine
	busy.Load1 = 20
	if got := Limit(Policy{}, busy, 5); got != 5 {
		t.Errorf("an overloaded machine must add no turn, got %d", got)
	}
	if got := Limit(Policy{}, Readings{Cores: 2, Load1: 0, MemTotal: 512 * gib, MemAvail: 500 * gib}, 0); got != 8 {
		t.Errorf("limit must stay under four turns per core, got %d", got)
	}
	if got := Limit(Policy{MaxTurns: 3}, busy, 9); got != 3 {
		t.Errorf("fixed limit = %d, want 3", got)
	}
	if got := Limit(Policy{Off: true}, busy, 9); got >= 0 {
		t.Errorf("off must not gate, got %d", got)
	}
}

// pool builds a gate with a fixed limit whose Wait runs the next step of a
// script. A script that runs out fails the test instead of waiting for real.
func pool(t *testing.T, limit int, steps ...func()) Gate {
	t.Helper()
	return Gate{
		Dir:       t.TempDir(),
		Limit:     func(Pool, int) int { return limit },
		RoomShare: RoomShare,
		Wait: func(context.Context) error {
			if len(steps) == 0 {
				t.Fatal("a turn waited with nothing left to free a slot")
			}
			step := steps[0]
			steps = steps[1:]
			step()
			return nil
		},
	}
}

func TestTurnWaitsForASlotAndNoticesOnce(t *testing.T) {
	var first func()
	g := pool(t, 2, func() { first() })
	r1, err := g.Acquire(t.Context(), Turns, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	first = r1
	must(t, g, Turns, "b")
	holds := 0
	r3, err := g.Acquire(t.Context(), Turns, "c", func(h Hold) {
		holds++
		if h.Limit != 2 || h.Held != 2 {
			t.Errorf("hold = %+v", h)
		}
	})
	if err != nil || holds != 1 {
		t.Fatalf("third turn: %v, %d notices", err, holds)
	}
	r3()
	r3() // a second release is harmless
}

func TestOneRoomCannotTakeMoreThanItsShare(t *testing.T) {
	var held []func()
	g := pool(t, 4, func() { held[0]() })
	for i := 0; i < 2; i++ {
		r, err := g.Acquire(t.Context(), Turns, "busy", nil)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, r)
	}
	// Two of four slots are free, yet the busy room holds its half and waits.
	var hold Hold
	if _, err := g.Acquire(t.Context(), Turns, "busy", func(h Hold) { hold = h }); err != nil || !hold.RoomFull {
		t.Fatalf("busy room: %v, hold %+v", err, hold)
	}
	// Another room is admitted at once. The script has no step left to wait on.
	must(t, g, Turns, "other")
}

// A parent turn that waits on its leaf sessions holds a slot of the turn pool.
// Leaves draw from their own pool, so neither waits behind the other, and a
// room that fills its leaf share does not block another room's leaves.
func TestParentAndLeavesNeverWaitOnEachOther(t *testing.T) {
	g := pool(t, 2)
	for _, room := range []string{"run-room", "other-room"} {
		must(t, g, Turns, room)
	}
	// The turn pool is full now. A third turn would wait; the script has no
	// step to free a slot, so the leaves below must not touch that pool.
	must(t, g, Leaves, "run-room")
	must(t, g, Leaves, "other-room")
}

func TestOffLimitTakesNoSlot(t *testing.T) {
	g := pool(t, -1)
	for i := 0; i < 5; i++ {
		must(t, g, Turns, "a")
	}
}

// Slots 0 and 1 are held. Slot 0 ends and the limit falls to 1: one turn still
// runs, so no new one may take the free slot 0.
func TestAFallingLimitAdmitsNobodyUntilEnoughTurnsEnd(t *testing.T) {
	limit := 2
	waited := 0
	g := Gate{
		Dir:       t.TempDir(),
		Limit:     func(Pool, int) int { return limit },
		RoomShare: func(l int) int { return l },
		Wait:      func(context.Context) error { waited++; return context.Canceled },
	}
	first, err := g.Acquire(t.Context(), Turns, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.Acquire(t.Context(), Turns, "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second) // the release closure owns the slot's file; keep it alive
	first()
	limit = 1
	if _, err := g.Acquire(t.Context(), Turns, "c", nil); err == nil || waited != 1 {
		t.Fatalf("a turn was admitted over a limit of 1 with one turn running: %v, waited %d", err, waited)
	}
}

// Two runners of one room race for an empty pool whose room share is one. The
// first is parked inside the allocation step until the second has started, so
// the second can only run its policy check after the first holds its slot.
func TestRacingRunnersOfOneRoomShareOneSlot(t *testing.T) {
	secondStarted := make(chan struct{})
	firstParked := make(chan struct{})
	var calls atomic.Int32
	g := Gate{
		Dir: t.TempDir(),
		Limit: func(_ Pool, held int) int {
			if calls.Add(1) == 1 { // the first runner, inside the allocation lock
				close(firstParked)
				<-secondStarted
				return 2
			}
			// The second runner may only look once the first holds its slot.
			if held != 1 {
				t.Errorf("second runner saw %d held slots, want the first runner's slot", held)
			}
			return 2
		},
		RoomShare: RoomShare,
		Wait:      func(context.Context) error { return context.Canceled },
	}
	first := make(chan func(), 1)
	go func() {
		release, err := g.Acquire(t.Context(), Turns, "room", nil)
		if err != nil {
			t.Error(err)
			first <- func() {}
			return
		}
		first <- release
	}()
	<-firstParked
	second := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, err := g.Acquire(t.Context(), Turns, "room", nil)
		second <- err
	}()
	t.Cleanup(<-first) // the first runner keeps its slot until the test ends
	if err := <-second; err == nil {
		t.Fatal("a second runner of one room took the other slot despite a share of one")
	}
}

// must takes a slot that lasts until the test ends. The release closure owns
// the slot's file, so dropping it would let the garbage collector free the lock.
func must(t *testing.T, g Gate, pool Pool, room string) {
	t.Helper()
	release, err := g.Acquire(t.Context(), pool, room, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}
