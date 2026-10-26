package admission

import "time"

// Mode says what the admission watcher does. Only off and observe exist: the
// watcher records what a limit on concurrent turns would have held and holds
// nothing, so an owner sees the effect before any limit is built.
type Mode string

// The modes.
const (
	ModeOff     Mode = "off"
	ModeObserve Mode = "observe"
)

// SampleEvery is how often the engine takes a sample.
const SampleEvery = 45 * time.Second

// Tunables of the automatic limit. They describe agent turns in general, and
// the first is replaced by a measurement where one exists.
const (
	// FallbackTurnBytes is the memory one turn is assumed to use until Dyna has
	// measured real turns on this machine.
	FallbackTurnBytes = 1 << 30
	TurnsPerCore      = 4
	MinReserve        = 2 << 30
	reserveDivisor    = 10 // keep a tenth of memory free
	floorTurns        = 1
	// EnterMemPressure and EnterCPUPressure are the PSI "some avg10" values at
	// which the machine counts as overloaded. Without PSI, EnterLoad per core
	// stands in.
	EnterMemPressure = 10.0
	EnterCPUPressure = 40.0
	EnterLoad        = 2.0
	// CalmReads is how many readings in a row must sit below half of every
	// threshold before the machine counts as calm again.
	CalmReads = 2
)

// State is what the watcher remembers between samples, so a machine that flaps
// around a threshold does not flap the limit.
type State struct {
	Overloaded bool
	Calm       int
	// TurnBytes is the measured memory of one turn, and MeasuredAt when.
	TurnBytes  uint64
	MeasuredAt time.Time
}

// over reports whether r is past the thresholds, scaled by factor: 1 for
// entering overload, 0.5 for the calm test.
func over(r Readings, factor float64) bool {
	if r.PressureMem >= 0 || r.PressureCPU >= 0 {
		return r.PressureMem >= EnterMemPressure*factor || r.PressureCPU >= EnterCPUPressure*factor
	}
	return r.Cores > 0 && r.Load1/float64(r.Cores) > EnterLoad*factor
}

// Decide returns the most turns that could run, given the turns that run now,
// and the state to keep. The limit adds the turns the free memory can still
// take to the turns that run; it adds none while the machine is overloaded, and
// an overload ends only after CalmReads calm readings. At least one turn always
// runs.
func Decide(r Readings, held int, st State) (int, State) {
	switch {
	case over(r, 1):
		st.Overloaded, st.Calm = true, 0
	case st.Overloaded && over(r, 0.5):
		st.Calm = 0
	case st.Overloaded:
		if st.Calm++; st.Calm >= CalmReads {
			st.Overloaded, st.Calm = false, 0
		}
	}
	if st.Overloaded {
		return max(held, floorTurns), st
	}
	per := st.TurnBytes
	if per == 0 {
		per = FallbackTurnBytes
	}
	reserve := max(r.MemTotal/reserveDivisor, MinReserve)
	room := 0
	if r.MemAvail > reserve {
		room = int((r.MemAvail - reserve) / per)
	}
	hard := max(r.Cores, 1) * TurnsPerCore
	return min(max(held+room, floorTurns), hard), st
}
