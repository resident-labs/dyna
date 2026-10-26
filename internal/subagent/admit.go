package subagent

import (
	"context"
	"log"
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
	// Owner is asked at every attempt and says whether the owner's own message
	// caused the turn. The owner is never made to wait, so a true answer admits
	// the turn at once, even after it began to wait.
	Owner func() bool
	// OnHold runs when the gate holds the turn.
	OnHold func(admission.Hold)
}

// Admit takes a slot for a turn before its harness starts and returns the call
// that frees it. An error is only the context's: the gate never fails a turn.
// A nil Admit admits every turn at once.
type Admit func(ctx context.Context, req AdmitRequest) (release func(), err error)

// admitPoll is how often a waiting turn looks for a free slot.
const admitPoll = time.Second

// admitBound bounds each platform read and database call the gate makes, so a
// stuck one cannot hold a turn.
const admitBound = 3 * time.Second

// MachineGate limits the turns that run at once on this machine by the owner's
// admission settings. Its fields are the seams a test replaces.
type MachineGate struct {
	Load    func() settings.Values
	Source  admission.Source
	Sampler admission.Sampler
	States  StateKeeper
	Dir     string
	Now     func() time.Time
	Sleep   func(ctx context.Context) error
	// Record stores one observation; it runs off the turn's path.
	Record func(context.Context, admission.Observation) error
	// Running counts the running turns of meta's pool: all of them, and those of
	// meta's room. Observe mode counts with it, because it takes no slot.
	Running func(ctx context.Context, meta Meta) (total, inRoom int, err error)
	// Async runs work off the turn's path. The default starts a goroutine.
	Async func(func())
}

// StateKeeper saves the gate's state between decisions.
type StateKeeper interface {
	Load(ctx context.Context, pool admission.Pool) (admission.State, error)
	Save(ctx context.Context, pool admission.Pool, st admission.State) error
}

// MachineAdmit returns the gate for m's machine, reading the real platform
// sources and the owner's settings.
func MachineAdmit(m *Manager) Admit {
	dataDir := storepkg.DataDir()
	source, sampler := admission.Detect()
	return MachineGate{
		Load: m.settings, Source: source, Sampler: sampler,
		States: admission.StateStore{DataDir: dataDir}, Dir: filepath.Join(dataDir, "admission"), Now: time.Now,
		Sleep: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(admitPoll):
				return nil
			}
		},
		Record:  admission.Observed{DataDir: dataDir}.Record,
		Running: m.runningTurns,
	}.Admit
}

// admitPool picks the pool of a session: a child draws from its own pool, so a
// parent that waits on a child never waits behind it.
func admitPool(meta Meta) admission.Pool {
	if meta.Leaf || meta.ParentSession != "" {
		return admission.Children
	}
	return admission.Turns
}

// Admit implements Admit. Room roots, service sessions and owner-caused turns
// are never held: a root often waits on its children, a service session is a
// part of how the machine runs, and the owner is never made to wait. In off and
// observe mode no turn takes a slot, a lock or a synchronous write, so the gate
// cannot delay or fail a turn. In enforce mode every gate error and every
// unreadable source lets the turn run.
func (g MachineGate) Admit(ctx context.Context, req AdmitRequest) (func(), error) {
	noop := func() {}
	meta := req.Meta
	if meta.Role == RoleRoot || meta.Service || g.Source == nil {
		return noop, nil
	}
	policy := PolicyOf(g.Load())
	switch policy.Mode {
	case admission.ModeEnforce:
	case admission.ModeObserve:
		g.async(func() { g.observe(meta, policy) })
		return noop, nil
	default:
		return noop, nil
	}
	pool := admitPool(meta)
	gate := admission.Gate{
		Dir:       g.Dir,
		RoomShare: admission.RoomShare,
		Wait:      g.Sleep,
		MaxWait:   policy.MaxWait,
		Now:       g.Now,
		Bypass:    req.Owner,
		Limit: func(pool admission.Pool, slots []admission.Slot) int {
			bound, cancel := context.WithTimeout(ctx, admitBound)
			defer cancel()
			readings, err := g.Source.Read(bound)
			if err != nil {
				return -1 // a source that cannot be read gates nothing
			}
			st, err := g.States.Load(bound, pool)
			if err != nil {
				st = admission.State{}
			}
			limit, st := admission.Decide(policy, readings, len(slots), st)
			st = g.measure(bound, st, slots)
			if err := g.States.Save(bound, pool, st); err != nil {
				log.Printf("admission: save state: %v", err)
			}
			return limit
		},
	}
	since := g.Now()
	release, err := gate.Acquire(ctx, pool, meta.RoomID, func(h admission.Hold) {
		if !h.Forced {
			g.async(func() { g.record(meta, since, h) })
			log.Printf("admission: %s turn of %s held: %d of %d slots in use (%s)", pool, meta.ID, h.Held, h.Limit, h.Reason)
		}
		if req.OnHold != nil {
			req.OnHold(h)
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		log.Printf("admission: %v; the turn runs without a slot", err)
		return noop, nil
	}
	return release, nil
}

func (g MachineGate) async(f func()) {
	if g.Async != nil {
		g.Async(f)
		return
	}
	go f()
}

func (g MachineGate) record(meta Meta, at time.Time, h admission.Hold) {
	if g.Record == nil {
		return
	}
	bound, cancel := context.WithTimeout(context.Background(), admitBound)
	defer cancel()
	if err := g.Record(bound, admission.Observation{At: at, Pool: h.Pool, Room: meta.RoomID, Reason: h.Reason, Held: h.Held, Limit: h.Limit}); err != nil {
		log.Printf("admission: record: %v", err)
	}
}

// observe judges, off the turn's path, whether enforcing would hold the turn,
// from the turns running now. It takes no slot and no lock.
func (g MachineGate) observe(meta Meta, policy admission.Policy) {
	if g.Running == nil || g.States == nil {
		return
	}
	bound, cancel := context.WithTimeout(context.Background(), admitBound)
	defer cancel()
	readings, err := g.Source.Read(bound)
	if err != nil {
		return
	}
	total, inRoom, err := g.Running(bound, meta)
	if err != nil {
		return
	}
	pool := admitPool(meta)
	st, err := g.States.Load(bound, pool)
	if err != nil {
		return
	}
	limit, st := admission.Decide(policy, readings, total, st)
	if err := g.States.Save(bound, pool, st); err != nil {
		log.Printf("admission: save state: %v", err)
	}
	hold := admission.Hold{Pool: pool, Limit: limit, Held: total}
	switch {
	case total >= limit:
		hold.Reason = admission.ReasonLimit
	case inRoom >= admission.RoomShare(limit):
		hold.Reason = admission.ReasonRoomShare
	default:
		return
	}
	log.Printf("admission: observe: a %s turn of %s would be held: %d of %d turns running (%s)", pool, meta.ID, total, limit, hold.Reason)
	g.record(meta, g.Now(), hold)
}

// measure refreshes the measured memory of one turn from the runners that
// hold slots, at most once a minute and only with two or more to average.
func (g MachineGate) measure(ctx context.Context, st admission.State, slots []admission.Slot) admission.State {
	if g.Sampler == nil || len(slots) < 2 || g.Now().Sub(st.MeasuredAt) < time.Minute {
		return st
	}
	pids := make([]int, 0, len(slots))
	for _, s := range slots {
		if s.PID > 0 && !slices.Contains(pids, s.PID) {
			pids = append(pids, s.PID)
		}
	}
	if per := g.Sampler.Measure(ctx, pids); per > 0 {
		st.TurnBytes = min(max(per, 128<<20), 16<<30)
		st.MeasuredAt = g.Now()
	}
	return st
}

// runningTurns counts the running turns that share meta's pool, and those in
// meta's room. The asking session is not counted.
func (m *Manager) runningTurns(_ context.Context, meta Meta) (total, inRoom int, err error) {
	infos, err := m.store().ListSnapshot(false)
	if err != nil {
		return 0, 0, err
	}
	pool := admitPool(meta)
	for _, in := range infos {
		if in.ID == meta.ID || in.Status != StatusRunning || in.Role == RoleRoot || in.Service || admitPool(in.Meta) != pool {
			continue
		}
		total++
		if in.RoomID == meta.RoomID {
			inRoom++
		}
	}
	return total, inRoom, nil
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
