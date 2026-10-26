package engine

import (
	"context"
	"strings"
	"time"

	"dyna-agent/internal/admission"
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
