package subagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/admission"
	"dyna-agent/internal/settings"
)

// fakeSource answers with fixed readings.
type fakeSource struct {
	r   admission.Readings
	err error
}

func (f fakeSource) Read(context.Context) (admission.Readings, error) { return f.r, f.err }

// memoryState keeps the gate's state in memory.
type memoryState struct{ st admission.State }

func (m *memoryState) Load(context.Context, admission.Pool) (admission.State, error) {
	return m.st, nil
}
func (m *memoryState) Save(_ context.Context, _ admission.Pool, st admission.State) error {
	m.st = st
	return nil
}

var roomy = admission.Readings{Cores: 8, MemTotal: 64 << 30, MemAvail: 60 << 30, PressureCPU: 0, PressureMem: 0}

// gate is a machine gate with a fixed policy, a clock the sleep moves, and no
// goroutines or timers. Record collects what the gate stores.
type gate struct {
	MachineGate
	seen *[]admission.Observation
}

func newGate(t *testing.T, mode settings.AdmissionMode, maxTurns string, src admission.Source) gate {
	t.Helper()
	var seen []admission.Observation
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	return gate{MachineGate: MachineGate{
		Load: func() settings.Values {
			v := settings.Defaults()
			v.AdmissionMode, v.AdmissionMaxTurns = mode, maxTurns
			return v
		},
		Source: src, States: &memoryState{}, Dir: filepath.Join(t.TempDir(), "admission"),
		Now:    func() time.Time { return clock },
		Sleep:  func(context.Context) error { clock = clock.Add(time.Minute); return nil },
		Record: func(_ context.Context, o admission.Observation) error { seen = append(seen, o); return nil },
		Async:  func(f func()) { f() },
	}, seen: &seen}
}

func (g gate) take(t *testing.T, req AdmitRequest) func() {
	t.Helper()
	release, err := g.Admit(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}

// With one slot taken, a room root, a service session, an owner-caused turn
// and a child start at once. A second ordinary turn waits, and runs after
// MaxWait.
func TestEnforcedGateHoldsOnlyOrdinaryTurns(t *testing.T) {
	g := newGate(t, settings.AdmissionEnforce, "1", fakeSource{r: roomy})
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, req := range map[string]AdmitRequest{
		"root":    {Meta: Meta{ID: "ses_r", RoomID: "room-b", Role: RoleRoot}},
		"service": {Meta: Meta{ID: "ses_s", RoomID: "room-b", Service: true}},
		"owner":   {Meta: Meta{ID: "ses_o", RoomID: "room-b"}, Owner: func() bool { return true }},
		"child":   {Meta: Meta{ID: "ses_c", RoomID: "room-b", ParentSession: "ses_a"}},
		"leaf":    {Meta: Meta{ID: "ses_l", RoomID: "room-b", Leaf: true}},
	} {
		if r, err := g.Admit(cancelled, req); err != nil {
			t.Errorf("%s turn was held: %v", name, err)
		} else {
			r()
		}
	}
	var holds []admission.Hold
	// MaxWait is two minutes and each sleep moves the clock one, so the turn
	// waits twice and then runs.
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_b", RoomID: "room-b"}, OnHold: func(h admission.Hold) { holds = append(holds, h) }})
	if len(holds) != 2 || holds[0].Forced || !holds[1].Forced || holds[0].Reason != admission.ReasonLimit {
		t.Fatalf("holds = %+v, want a wait, then a forced start after MaxWait", holds)
	}
	if len(*g.seen) != 1 || (*g.seen)[0].Reason != admission.ReasonLimit {
		t.Fatalf("recorded %+v, want one row for the held turn", *g.seen)
	}
}

// The owner posts while the turn waits: the next attempt lets it in.
func TestAnOwnerMessageDuringTheWaitAdmitsTheTurn(t *testing.T) {
	g := newGate(t, settings.AdmissionEnforce, "1", fakeSource{r: roomy})
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
	owner := false
	g.Sleep = func(context.Context) error { owner = true; return nil }
	var holds int
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_b", RoomID: "room-b"}, Owner: func() bool { return owner }, OnHold: func(admission.Hold) { holds++ }})
	if holds != 1 {
		t.Fatalf("holds = %d, want the turn to wait once and then be let in", holds)
	}
}

// Observe takes no slot, no lock and no file: it counts the running turns and
// records, off the turn's path, what enforcing would have held.
func TestObserveTakesNothingAndRecordsWhatItWouldHold(t *testing.T) {
	g := newGate(t, settings.AdmissionObserve, "2", fakeSource{r: roomy})
	g.Running = func(context.Context, Meta) (int, int, error) { return 3, 1, nil }
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_b", RoomID: "room-b"}})
	if len(*g.seen) != 1 || (*g.seen)[0].Reason != admission.ReasonLimit || (*g.seen)[0].Held != 3 || (*g.seen)[0].Limit != 2 {
		t.Fatalf("observations = %+v", *g.seen)
	}
	if _, err := os.Stat(g.Dir); !os.IsNotExist(err) {
		t.Fatalf("observe created the gate directory: %v", err)
	}
	g.Running = func(context.Context, Meta) (int, int, error) { return 1, 0, nil }
	*g.seen = nil
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_c", RoomID: "room-b"}})
	if len(*g.seen) != 0 {
		t.Fatalf("a turn under the limit was recorded: %+v", *g.seen)
	}
}

// No failure of the sources, the counters or the database reaches the turn.
func TestObserveNeverFailsOrDelaysATurn(t *testing.T) {
	for name, mutate := range map[string]func(*gate){
		"unreadable source": func(g *gate) { g.Source = fakeSource{err: errors.New("no /proc")} },
		"counter fails": func(g *gate) {
			g.Running = func(context.Context, Meta) (int, int, error) { return 0, 0, errors.New("boom") }
		},
		"record fails": func(g *gate) {
			g.Running = func(context.Context, Meta) (int, int, error) { return 9, 9, nil }
			g.Record = func(context.Context, admission.Observation) error { return errors.New("database is locked") }
		},
	} {
		g := newGate(t, settings.AdmissionObserve, "1", fakeSource{r: roomy})
		g.Running = func(context.Context, Meta) (int, int, error) { return 0, 0, nil }
		mutate(&g)
		r, err := g.Admit(t.Context(), AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		r()
	}
}

func TestOffAndUnreadableSourcesGateNothing(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, g := range map[string]gate{
		"off":        newGate(t, settings.AdmissionOff, "1", fakeSource{r: roomy}),
		"unreadable": newGate(t, settings.AdmissionEnforce, "1", fakeSource{err: errors.New("no /proc")}),
		"no source":  newGate(t, settings.AdmissionEnforce, "1", nil),
	} {
		for i := 0; i < 3; i++ {
			r, err := g.Admit(cancelled, AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
			if err != nil {
				t.Fatalf("%s: turn %d was held: %v", name, i, err)
			}
			t.Cleanup(r)
		}
	}
}

// A data folder that cannot hold the gate's files lets the turn run.
func TestEnforceFailsOpenWhenTheGateCannotStore(t *testing.T) {
	g := newGate(t, settings.AdmissionEnforce, "1", fakeSource{r: roomy})
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.Dir = filepath.Join(blocker, "admission") // a path under a regular file
	g.take(t, AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
}

func TestChildrenDrawFromTheirOwnPool(t *testing.T) {
	if admitPool(Meta{}) != admission.Turns || admitPool(Meta{ParentSession: "ses_p"}) != admission.Children || admitPool(Meta{Leaf: true}) != admission.Children {
		t.Fatal("a session with a parent, or a leaf, must use the children pool and a top-level one the turns pool")
	}
}

// An agent's child is not owner-caused just because its Owner field holds the
// parent's session ID; only an owner's own label counts, on the first turn of a
// session that no agent started.
func TestOwnerCausedFirstTurnNeedsAnOwnerLabelAndNoParent(t *testing.T) {
	m := &Manager{}
	turn := Turn{N: 1}
	if !m.ownerCaused(Meta{Owner: "claude:abc"}, turn) {
		t.Error("a first turn started by an owner caller is owner-caused")
	}
	if m.ownerCaused(Meta{Owner: NewID(), ParentSession: "ses_p"}, turn) || m.ownerCaused(Meta{Owner: NewID()}, turn) {
		t.Error("a child started by a session is not owner-caused")
	}
}

// A turn in a paused room holds no machine slot: it gives the slot back when
// the room holds it and takes one again when the room resumes.
func TestRoomHoldGivesTheMachineSlotBack(t *testing.T) {
	e := roomControlEnv(t)
	a := completedFixtureSession(t, e)
	if _, err := e.m.Store.UpdateTurn(a.ID, 1, func(t *Turn) { t.Status = TurnRunning; t.Ended = time.Time{} }); err != nil {
		t.Fatal(err)
	}
	var held []string
	// The slot has its own manager, so a runner that the resume launches in
	// this environment cannot add events to the list.
	slotManager := &Manager{Admit: func(context.Context, AdmitRequest) (func(), error) {
		held = append(held, "take")
		return func() { held = append(held, "free") }, nil
	}}
	slot := &turnSlot{m: slotManager, meta: Meta{ID: a.ID, RoomID: a.RoomID}}
	if err := slot.take(e.ctx); err != nil {
		t.Fatal(err)
	}
	writes := e.m.runnerWrites(a.ID, 1)
	writes.suspend, writes.resume = slot.drop, slot.take
	if _, err := e.m.PauseRoomSessions(e.ctx, a.RoomID); err != nil {
		t.Fatal(err)
	}
	e.m.TransferSleep = func(ctx context.Context, _ time.Duration) error {
		if got := strings.Join(held, ","); got != "take,free" {
			t.Errorf("slot events while the room is held = %s, want take,free", got)
		}
		_, err := e.m.ResumeRoomSessions(ctx, a.RoomID)
		return err
	}
	if err := writes.waitSession(e.ctx, "prompt"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(held, ","); got != "take,free,take" {
		t.Fatalf("slot events = %s, want take,free,take", got)
	}
}
