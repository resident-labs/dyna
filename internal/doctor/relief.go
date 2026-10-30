package doctor

import (
	"context"
	"log"
	"slices"
	"time"
)

// MinUnloadIdle is the least time an agent process must sit idle before memory
// pressure may stop it. It only avoids stopping and starting the same process
// again and again: a session that just finished is the likeliest to get a
// message soon, and a start costs seconds. Unloading loses no model cache.
const MinUnloadIdle = 5 * time.Minute

// MaxUnloadsPerSample bounds one sample's relief, so a sample never stops a
// whole machine of sessions on one reading. The next sample reads memory again.
const MaxUnloadsPerSample = 3

// Idle is one agent process that holds memory and has no turn attached. H is
// whatever its host needs to stop it.
type Idle[H any] struct {
	// Handle names it for Unload.
	Handle H
	// Session is the Dyna session that owns it.
	Session string
	// Idle is how long it has been quiet.
	Idle time.Duration
}

// Relief stops idle agent processes when memory is short.
type Relief[H any] struct {
	// List returns the idle processes.
	List func(ctx context.Context) ([]Idle[H], error)
	// Busy reports whether the session has a turn running or queued, or input
	// waiting. A busy session is never unloaded.
	Busy func(session string) bool
	// Unload stops one process if it is still idle for at least minIdle, and
	// reports whether it did.
	Unload func(ctx context.Context, p Idle[H], minIdle time.Duration) (bool, error)
}

// Run stops up to MaxUnloadsPerSample processes, the longest idle first, and
// returns how many it stopped. A process idle under MinUnloadIdle is left.
func (r Relief[H]) Run(ctx context.Context) int {
	idle, err := r.List(ctx)
	if err != nil {
		log.Printf("memory relief: list idle agent processes: %v", err)
	}
	slices.SortFunc(idle, func(a, b Idle[H]) int { return int(b.Idle - a.Idle) })
	stopped := 0
	for _, p := range idle {
		if stopped >= MaxUnloadsPerSample || ctx.Err() != nil || p.Idle < MinUnloadIdle {
			break
		}
		if r.Busy(p.Session) {
			continue
		}
		done, err := r.Unload(ctx, p, MinUnloadIdle)
		if err != nil {
			log.Printf("memory relief: unload idle agent process of %s: %v", p.Session, err)
			continue
		}
		if done {
			stopped++
		}
	}
	return stopped
}

// MemoryShort reports memory pressure or available memory below the reserve.
func MemoryShort(r Readings) bool {
	return r.PressureMem >= 10 || r.MemTotal > 0 && r.MemAvail < max(r.MemTotal/10, 2<<30)
}

// Check reads memory and stops idle processes only under pressure.
func (r Relief[H]) Check(ctx context.Context, source Source) error {
	reading, err := source.Read(ctx)
	if err != nil {
		return err
	}
	if MemoryShort(reading) {
		if n := r.Run(ctx); n > 0 {
			log.Printf("memory relief: stopped %d idle agent processes", n)
		}
	}
	return nil
}
