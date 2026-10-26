package subagent

import (
	"context"
	"errors"
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

var roomy = admission.Readings{Cores: 8, MemTotal: 64 << 30, MemAvail: 60 << 30, PressureCPU: 0, PressureMem: 0}

// gateFor builds a machine gate with a fixed policy and a clock the test moves.
func gateFor(t *testing.T, mode string, maxTurns string, src admission.Source) (MachineGate, *[]admission.Observation) {
	t.Helper()
	var seen []admission.Observation
	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	return MachineGate{
		Load: func() settings.Values {
			v := settings.Defaults()
			v.AdmissionMode, v.AdmissionMaxTurns = mode, maxTurns
			return v
		},
		Source: src, Dir: t.TempDir(), ProcDir: t.TempDir(),
		Now:    func() time.Time { return clock },
		Sleep:  func(context.Context) error { clock = clock.Add(time.Minute); return nil },
		Record: func(o admission.Observation) error { seen = append(seen, o); return nil },
	}, &seen
}

func hold(t *testing.T, g MachineGate, req AdmitRequest) func() {
	t.Helper()
	release, err := g.Admit(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}

// With one slot taken, a room root, a service session, an owner-caused turn and
// a leaf start at once. A second ordinary turn waits, and starts after MaxWait.
func TestEnforcedGateHoldsOnlyOrdinaryTurns(t *testing.T) {
	g, seen := gateFor(t, settings.AdmissionEnforce, "1", fakeSource{r: roomy})
	hold(t, g, AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, req := range map[string]AdmitRequest{
		"root":    {Meta: Meta{ID: "ses_r", RoomID: "room-b", Role: RoleRoot}},
		"service": {Meta: Meta{ID: "ses_s", RoomID: "room-b", Service: true}},
		"owner":   {Meta: Meta{ID: "ses_o", RoomID: "room-b"}, Owner: true},
		"leaf":    {Meta: Meta{ID: "ses_l", RoomID: "room-b", Leaf: true}},
	} {
		if r, err := g.Admit(cancelled, req); err != nil {
			t.Errorf("%s turn was held: %v", name, err)
		} else {
			r()
		}
	}
	var holds []admission.Hold
	// The sleep moves the clock one minute; MaxWait is two, so the turn waits twice.
	hold(t, g, AdmitRequest{Meta: Meta{ID: "ses_b", RoomID: "room-b"}, OnHold: func(h admission.Hold) { holds = append(holds, h) }})
	if len(holds) != 2 || holds[0].Forced || !holds[1].Forced {
		t.Fatalf("holds = %+v, want a wait, then a forced start after MaxWait", holds)
	}
	if len(*seen) != 1 {
		t.Fatalf("recorded %d observations, want one for the held turn", len(*seen))
	}
}

func TestObserveHoldsNothingAndRecordsWhatItWouldHold(t *testing.T) {
	g, seen := gateFor(t, settings.AdmissionObserve, "1", fakeSource{r: roomy})
	hold(t, g, AdmitRequest{Meta: Meta{ID: "ses_a", RoomID: "room-a"}})
	var holds []admission.Hold
	hold(t, g, AdmitRequest{Meta: Meta{ID: "ses_b", RoomID: "room-b"}, OnHold: func(h admission.Hold) { holds = append(holds, h) }})
	if len(holds) != 1 || !holds[0].Observed || len(*seen) != 1 || (*seen)[0].Held != 1 || (*seen)[0].Limit != 1 {
		t.Fatalf("holds %+v, observations %+v", holds, *seen)
	}
}

func TestOffAndUnreadableSourcesGateNothing(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, g := range map[string]MachineGate{
		"off":        mustGate(gateFor(t, settings.AdmissionOff, "1", fakeSource{r: roomy})),
		"unreadable": mustGate(gateFor(t, settings.AdmissionEnforce, "1", fakeSource{err: errors.New("no /proc")})),
		"no source":  mustGate(gateFor(t, settings.AdmissionEnforce, "1", nil)),
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

func mustGate(g MachineGate, _ *[]admission.Observation) MachineGate { return g }

func TestOwnerCausedTurnsAreRecognized(t *testing.T) {
	meta := Meta{Room: "work", Owner: ""}
	if !ownerCaused(meta, Turn{N: 3, ReportTo: []string{"work"}}) {
		t.Error("a turn that owes the room's owner a report is owner-caused")
	}
	if ownerCaused(meta, Turn{N: 3, ReportTo: []string{"parent-session"}}) {
		t.Error("a turn caused by a parent session is not owner-caused")
	}
	if !ownerCaused(Meta{Room: "work", Owner: "claude:abc"}, Turn{N: 1}) {
		t.Error("a first turn started by an owner caller is owner-caused")
	}
}

// A turn in a paused room holds no machine slot: it gives the slot back when
// the room holds it and takes one again when the room resumes.
func TestRoomHoldGivesTheMachineSlotBack(t *testing.T) {
	e := roomControlEnv(t)
	a, _ := e.start("RESPOND: hi")
	a, _ = e.wait(a.ID, 1)
	e.m.Store.UpdateTurn(a.ID, 1, func(t *Turn) { t.Status = TurnRunning; t.Ended = time.Time{} })
	var held []string
	// The slot has its own manager, so a runner that the resume launches in
	// this environment cannot add events to the list.
	gate := &Manager{Admit: func(context.Context, AdmitRequest) (func(), error) {
		held = append(held, "take")
		return func() { held = append(held, "free") }, nil
	}}
	slot := &turnSlot{m: gate, meta: Meta{ID: a.ID, RoomID: a.RoomID}}
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
