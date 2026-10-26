package admission

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

const gib = 1 << 30

func TestDecideFollowsMemoryAndPressure(t *testing.T) {
	calm := Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 20 * gib, PressureCPU: 1, PressureMem: 1}
	// The reserve is a tenth of 32 GiB; the other free memory takes one turn per GiB.
	if got, _ := Decide(Policy{}, calm, 3, State{}); got != 3+int((20*gib-32*gib/10)/gib) {
		t.Errorf("memory-bound limit = %d", got)
	}
	// A measured 4 GiB turn replaces the 1 GiB guess.
	if got, _ := Decide(Policy{}, calm, 3, State{TurnBytes: 4 * gib}); got != 3+int((20*gib-32*gib/10)/(4*gib)) {
		t.Errorf("measured limit = %d", got)
	}
	tight := calm
	tight.MemAvail = gib
	if got, _ := Decide(Policy{}, tight, 0, State{}); got != 1 {
		t.Errorf("a full machine must still run one turn, got %d", got)
	}
	if got, _ := Decide(Policy{}, Readings{Cores: 2, MemTotal: 512 * gib, MemAvail: 500 * gib, PressureCPU: -1, PressureMem: -1}, 0, State{}); got != 8 {
		t.Errorf("limit must stay under four turns per core, got %d", got)
	}
	if got, _ := Decide(Policy{MaxTurns: 3}, calm, 9, State{}); got != 3 {
		t.Errorf("fixed limit = %d, want 3", got)
	}
}

// Pressure above the threshold adds no turn. The machine counts as calm again
// only after CalmReads readings below half of every threshold, so a machine
// that flaps around the threshold does not flap the limit.
func TestOverloadHasHysteresis(t *testing.T) {
	r := Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 20 * gib, PressureCPU: 0, PressureMem: 12}
	st := State{}
	limit, st := Decide(Policy{}, r, 5, st)
	if limit != 5 || !st.Overloaded {
		t.Fatalf("under pressure: limit %d, state %+v", limit, st)
	}
	r.PressureMem = 7 // below the threshold, above half of it
	if limit, st = Decide(Policy{}, r, 5, st); limit != 5 || !st.Overloaded {
		t.Fatalf("still between half and full: limit %d, state %+v", limit, st)
	}
	r.PressureMem = 2
	if limit, st = Decide(Policy{}, r, 5, st); limit != 5 || !st.Overloaded {
		t.Fatalf("one calm reading must not end the overload: limit %d, state %+v", limit, st)
	}
	if limit, st = Decide(Policy{}, r, 5, st); limit <= 5 || st.Overloaded {
		t.Fatalf("two calm readings end it: limit %d, state %+v", limit, st)
	}
}

func TestLoadStandsInWhenThereIsNoPressureData(t *testing.T) {
	r := Readings{Cores: 4, Load1: 12, MemTotal: 32 * gib, MemAvail: 20 * gib, PressureCPU: -1, PressureMem: -1}
	if limit, st := Decide(Policy{}, r, 2, State{}); limit != 2 || !st.Overloaded {
		t.Fatalf("load 3 per core: limit %d, state %+v", limit, st)
	}
}

// pool builds a gate with a fixed limit whose Wait runs the next step of a
// script. A script that runs out fails the test instead of waiting for real.
func pool(t *testing.T, limit int, steps ...func()) Gate {
	t.Helper()
	return Gate{
		Dir:       t.TempDir(),
		Limit:     func(Pool, []Slot) int { return limit },
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
		Limit:     func(Pool, []Slot) int { return limit },
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
		Limit: func(_ Pool, slots []Slot) int {
			held := len(slots)
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

func TestObserveLetsEveryTurnInAndReportsTheHold(t *testing.T) {
	g := pool(t, 1)
	g.Observe = true
	must(t, g, Turns, "a")
	var hold Hold
	release, err := g.Acquire(t.Context(), Turns, "b", func(h Hold) { hold = h })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if !hold.Observed || hold.Held != 1 || hold.Limit != 1 {
		t.Fatalf("observed hold = %+v", hold)
	}
}

// A turn that waited MaxWait runs anyway, so no turn starves. The clock is a
// field the test advances; the wait step moves it.
func TestAWaitingTurnRunsAfterMaxWait(t *testing.T) {
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	g := Gate{
		Dir:       t.TempDir(),
		Limit:     func(Pool, []Slot) int { return 1 },
		RoomShare: RoomShare,
		MaxWait:   2 * time.Minute,
		Now:       func() time.Time { return clock },
		Wait:      func(context.Context) error { clock = clock.Add(time.Minute); return nil },
	}
	must(t, g, Turns, "a")
	var holds []Hold
	release, err := g.Acquire(t.Context(), Turns, "b", func(h Hold) { holds = append(holds, h) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if len(holds) != 2 || holds[0].Forced || !holds[1].Forced {
		t.Fatalf("holds = %+v, want a wait then a forced start", holds)
	}
}
