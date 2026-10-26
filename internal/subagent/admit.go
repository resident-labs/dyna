package subagent

import (
	"context"
	"path/filepath"
	"strconv"
	"time"

	"dyna-agent/internal/admission"
	"dyna-agent/internal/settings"
	storepkg "dyna-agent/internal/store"
)

// Admit takes a slot for a turn before its harness starts and returns the call
// that frees it. A nil Admit admits every turn at once.
type Admit func(ctx context.Context, meta Meta) (release func(), err error)

// admitPoll is how often a waiting turn looks for a free slot.
const admitPoll = time.Second

// MachineAdmit limits the turns that run at once on this machine by the
// owner's admission settings. Room roots and service sessions are never held:
// a root often waits on its children, and a service session is a part of how
// the machine runs. Leaf sessions draw from their own pool, so a parent that
// waits on them never waits behind them.
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
	return func(ctx context.Context, meta Meta) (func(), error) {
		if meta.Role == RoleRoot || meta.Service {
			return func() {}, nil
		}
		pool := admission.Turns
		if meta.Leaf {
			pool = admission.Leaves
		}
		gate := admission.Gate{
			Dir:       filepath.Join(storepkg.DataDir(), "admission"),
			RoomShare: admission.RoomShare,
			Wait: func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(admitPoll):
					return nil
				}
			},
			Limit: func(_ admission.Pool, held int) int {
				policy := PolicyOf(load())
				if policy.Off {
					return -1
				}
				readings, err := admission.Read()
				if err != nil {
					return -1 // a machine that cannot be read is not gated
				}
				return admission.Limit(policy, readings, held)
			},
		}
		return gate.Acquire(ctx, pool, meta.RoomID, nil)
	}
}

// PolicyOf reads the owner's admission settings.
func PolicyOf(v settings.Values) admission.Policy {
	p := admission.Policy{Reserve: uint64(max(v.AdmissionReserve, 0))}
	switch v.AdmissionMaxTurns {
	case settings.AdmissionOff:
		p.Off = true
	case settings.AdmissionAuto, "":
	default:
		p.MaxTurns, _ = strconv.Atoi(v.AdmissionMaxTurns)
	}
	return p
}
