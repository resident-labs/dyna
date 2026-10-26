package subagent

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"dyna-agent/internal/admission"
	"dyna-agent/internal/settings"
	storepkg "dyna-agent/internal/store"
)

// AdmitRequest is one turn asking for a slot.
type AdmitRequest struct {
	Meta Meta
	// Owner is true when the owner's own message caused the turn. The owner is
	// never made to wait, so such a turn is always let in.
	Owner bool
	// OnHold runs when the gate holds the turn, or in observe mode when it
	// would have.
	OnHold func(admission.Hold)
}

// Admit takes a slot for a turn before its harness starts and returns the call
// that frees it. A nil Admit admits every turn at once.
type Admit func(ctx context.Context, req AdmitRequest) (release func(), err error)

// admitPoll is how often a waiting turn looks for a free slot.
const admitPoll = time.Second

// MachineGate limits the turns that run at once on this machine by the owner's
// admission settings. Its fields are the seams a test replaces.
type MachineGate struct {
	Load    func() settings.Values
	Source  admission.Source
	Dir     string
	ProcDir string
	Now     func() time.Time
	Sleep   func(ctx context.Context) error
	Record  func(admission.Observation) error
}

// MachineAdmit returns the gate for this machine, reading the real platform
// sources and the owner's settings file. A nil load reads settings.json.
func MachineAdmit(load func() settings.Values) Admit {
	if load == nil {
		load = func() settings.Values {
			v, err := settings.Load(settings.DefaultPath())
			if err != nil {
				return settings.Defaults()
			}
			return v
		}
	}
	dir := filepath.Join(storepkg.DataDir(), "admission")
	return MachineGate{
		Load: load, Source: admission.Detect(), Dir: dir, ProcDir: "/proc", Now: time.Now,
		Sleep: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(admitPoll):
				return nil
			}
		},
		Record: admission.Observed{DataDir: storepkg.DataDir()}.Record,
	}.Admit
}

// Admit implements Admit. Room roots, service sessions and owner-caused turns
// are never held: a root often waits on its children, a service session is a
// part of how the machine runs, and the owner is never made to wait. Leaf
// sessions draw from their own pool, so a parent that waits on them never waits
// behind them. A machine that cannot be read lets every turn through.
func (g MachineGate) Admit(ctx context.Context, req AdmitRequest) (func(), error) {
	noop := func() {}
	meta := req.Meta
	if meta.Role == RoleRoot || meta.Service || req.Owner || g.Source == nil {
		return noop, nil
	}
	policy := PolicyOf(g.Load())
	if policy.Mode == admission.ModeOff {
		return noop, nil
	}
	pool := admission.Turns
	if meta.Leaf {
		pool = admission.Leaves
	}
	gate := admission.Gate{
		Dir:       g.Dir,
		RoomShare: admission.RoomShare,
		Wait:      g.Sleep,
		Observe:   policy.Mode == admission.ModeObserve,
		MaxWait:   policy.MaxWait,
		Now:       g.Now,
		Limit: func(pool admission.Pool, slots []admission.Slot) int {
			readings, err := g.Source.Read(ctx)
			if err != nil {
				return -1 // a source that cannot be read gates nothing
			}
			file := filepath.Join(g.Dir, string(pool), "state.json")
			st := readState(file)
			limit, st := admission.Decide(policy, readings, len(slots), st)
			st = g.measure(st, slots)
			writeState(file, st)
			return limit
		},
	}
	since := g.Now()
	return gate.Acquire(ctx, pool, meta.RoomID, func(h admission.Hold) {
		if !h.Forced {
			// One row per held turn: the wait that follows adds no row.
			if g.Record != nil {
				_ = g.Record(admission.Observation{At: since, Pool: pool, Room: meta.RoomID, Held: h.Held, Limit: h.Limit})
			}
			log.Printf("admission: %s turn of %s %s: %d of %d slots in use", pool, meta.ID, map[bool]string{true: "would be held", false: "held"}[h.Observed], h.Held, h.Limit)
		}
		if req.OnHold != nil {
			req.OnHold(h)
		}
	})
}

// measure refreshes the measured memory of one turn from the runners that
// hold slots, at most once a minute and only with two or more to average.
func (g MachineGate) measure(st admission.State, slots []admission.Slot) admission.State {
	if len(slots) < 2 || g.Now().Sub(st.MeasuredAt) < time.Minute {
		return st
	}
	pids := make([]int, 0, len(slots))
	for _, s := range slots {
		if s.PID > 0 && !slices.Contains(pids, s.PID) {
			pids = append(pids, s.PID)
		}
	}
	if per := admission.MeasureTurns(g.ProcDir, pids, uint64(os.Getpagesize())); per > 0 {
		st.TurnBytes = min(max(per, 128<<20), 16<<30)
		st.MeasuredAt = g.Now()
	}
	return st
}

func readState(file string) admission.State {
	var st admission.State
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func writeState(file string, st admission.State) {
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := file + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, file)
	}
}

// PolicyOf reads the owner's admission settings.
func PolicyOf(v settings.Values) admission.Policy {
	p := admission.Policy{Mode: admission.Mode(v.AdmissionMode), Reserve: uint64(max(v.AdmissionReserve, 0)), MaxWait: v.AdmissionMaxWait}
	if p.Mode == "" {
		p.Mode = admission.ModeObserve
	}
	if v.AdmissionMaxTurns != settings.AdmissionAuto && v.AdmissionMaxTurns != "" {
		p.MaxTurns, _ = strconv.Atoi(v.AdmissionMaxTurns)
	}
	return p
}
