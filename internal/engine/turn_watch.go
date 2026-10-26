package engine

import (
	"context"
	"strings"
	"time"

	"dyna-agent/internal/admission"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/sysload"
)

// admissionObserver builds the engine's background sampler of agent turns. It
// reads the owner's mode at each sample, counts running turns and agent
// sessions from the session store, and takes the memory of all agent processes
// from the load sampler.
func admissionObserver(sessions *subagent.Manager, load *sysload.Sampler, dataDir string) *admission.Observer {
	count := func(keep func(subagent.Info) bool) (int, error) {
		infos, err := sessions.Store.ListSnapshot(false)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, in := range infos {
			if keep(in) {
				n++
			}
		}
		return n, nil
	}
	return &admission.Observer{
		Source: admission.Detect(),
		States: admission.StateStore{DataDir: dataDir},
		Running: func(context.Context) (int, error) {
			return count(func(in subagent.Info) bool {
				return in.Status == subagent.StatusRunning && in.Role != subagent.RoleRoot && !in.Service
			})
		},
		// Memory and count come from one inventory: the sessions whose processes
		// the load sampler found alive in their cgroup scopes.
		AgentMemory: func(context.Context) (uint64, int) { return liveAgentMemory(load.Load().Groups) },
		Relieve:     idleRelief(sessions).Run,
		Record:      admission.Observed{DataDir: dataDir}.Record,
		Mode: func(context.Context) admission.Mode {
			v, err := settings.Load(settings.DefaultPath())
			if err != nil {
				return admission.ModeObserve
			}
			return admission.Mode(v.AdmissionMode)
		},
		Now: time.Now,
	}
}

// liveAgentMemory sums the memory of the agent sessions that have a live cgroup
// scope and counts them, so the average is over exactly the memory counted. A
// session with no live process (idle, dormant, or whose children have left) is
// not in either number.
func liveAgentMemory(groups []sysload.Group) (bytes uint64, agents int) {
	for _, g := range groups {
		if strings.HasPrefix(g.Name, "session:") {
			bytes += g.Memory
			agents++
		}
	}
	return bytes, agents
}

// sessionCallerPrefix starts the caller key of a Dyna session's harness process.
const sessionCallerPrefix = "session/"

// idleRelief stops idle harness processes of sessions that hold no turn work.
func idleRelief(sessions *subagent.Manager) admission.Relief[sessionhost.IdleSession] {
	return admission.Relief[sessionhost.IdleSession]{
		List: func(ctx context.Context) ([]admission.Idle[sessionhost.IdleSession], error) {
			hosted, err := sessionhost.IdleSessions(ctx)
			var out []admission.Idle[sessionhost.IdleSession]
			for _, h := range hosted {
				id, ok := strings.CutPrefix(h.Caller, sessionCallerPrefix)
				if !ok {
					continue // not a Dyna session: nothing says it is safe to stop
				}
				out = append(out, admission.Idle[sessionhost.IdleSession]{Handle: h, Session: id, Idle: h.Idle})
			}
			return out, err
		},
		Busy: sessions.HoldsTurnWork,
		Unload: func(ctx context.Context, p admission.Idle[sessionhost.IdleSession], minIdle time.Duration) (bool, error) {
			return sessionhost.UnloadIdle(ctx, p.Handle, minIdle)
		},
	}
}
