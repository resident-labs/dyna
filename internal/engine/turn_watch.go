package engine

import (
	"context"
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
		// Every open session that holds a process is in the agents group's memory,
		// roots and services included.
		Agents: func(context.Context) (int, error) {
			return count(func(in subagent.Info) bool {
				return in.Status == subagent.StatusRunning || in.Status == subagent.StatusQueued || in.Status == subagent.StatusIdle
			})
		},
		AgentBytes: func(context.Context) uint64 {
			for _, g := range load.Load().Groups {
				if g.Name == "agents" {
					return g.Memory
				}
			}
			return 0
		},
		Record: admission.Observed{DataDir: dataDir}.Record,
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
