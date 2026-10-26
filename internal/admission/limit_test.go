package admission

import "testing"

const gib = 1 << 30

func TestDecideFollowsMemoryAndPressure(t *testing.T) {
	calm := Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 20 * gib, PressureCPU: 1, PressureMem: 1}
	// The reserve is a tenth of 32 GiB; the other free memory takes one turn per GiB.
	if got, _ := Decide(calm, 3, State{}); got != 3+int((20*gib-32*gib/10)/gib) {
		t.Errorf("memory-bound limit = %d", got)
	}
	// A measured 4 GiB turn replaces the 1 GiB guess.
	if got, _ := Decide(calm, 3, State{TurnBytes: 4 * gib}); got != 3+int((20*gib-32*gib/10)/(4*gib)) {
		t.Errorf("measured limit = %d", got)
	}
	tight := calm
	tight.MemAvail = gib
	if got, _ := Decide(tight, 0, State{}); got != 1 {
		t.Errorf("a full machine must still allow one turn, got %d", got)
	}
	if got, _ := Decide(Readings{Cores: 2, MemTotal: 512 * gib, MemAvail: 500 * gib, PressureCPU: -1, PressureMem: -1}, 0, State{}); got != 8 {
		t.Errorf("limit must stay under four turns per core, got %d", got)
	}
}

// Pressure above the threshold adds no turn. The machine counts as calm again
// only after CalmReads readings below half of every threshold, so a machine
// that flaps around the threshold does not flap the limit.
func TestOverloadHasHysteresis(t *testing.T) {
	r := Readings{Cores: 8, MemTotal: 32 * gib, MemAvail: 20 * gib, PressureCPU: 0, PressureMem: 12}
	limit, st := Decide(r, 5, State{})
	if limit != 5 || !st.Overloaded {
		t.Fatalf("under pressure: limit %d, state %+v", limit, st)
	}
	r.PressureMem = 7 // below the threshold, above half of it
	if limit, st = Decide(r, 5, st); limit != 5 || !st.Overloaded {
		t.Fatalf("between half and full: limit %d, state %+v", limit, st)
	}
	r.PressureMem = 2
	if limit, st = Decide(r, 5, st); limit != 5 || !st.Overloaded {
		t.Fatalf("one calm reading must not end the overload: limit %d, state %+v", limit, st)
	}
	if limit, st = Decide(r, 5, st); limit <= 5 || st.Overloaded {
		t.Fatalf("two calm readings end it: limit %d, state %+v", limit, st)
	}
}

func TestLoadStandsInWhenThereIsNoPressureData(t *testing.T) {
	r := Readings{Cores: 4, Load1: 12, MemTotal: 32 * gib, MemAvail: 20 * gib, PressureCPU: -1, PressureMem: -1}
	if limit, st := Decide(r, 2, State{}); limit != 2 || !st.Overloaded {
		t.Fatalf("load 3 per core: limit %d, state %+v", limit, st)
	}
}
