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
// reads the owner's mode at each sample, counts the running turns from the
// session store, and takes the memory of all agent processes from the load
// sampler.
func admissionObserver(sessions *subagent.Manager, load *sysload.Sampler, dataDir string) admission.Observer {
	return admission.Observer{
		Source: admission.Detect(),
		States: admission.StateStore{DataDir: dataDir},
		Running: func(context.Context) (int, error) {
			infos, err := sessions.Store.ListSnapshot(false)
			if err != nil {
				return 0, err
			}
			running := 0
			for _, in := range infos {
				if in.Status == subagent.StatusRunning && in.Role != subagent.RoleRoot && !in.Service {
					running++
				}
			}
			return running, nil
		},
		AgentBytes: func() uint64 {
			for _, g := range load.Load().Groups {
				if g.Name == "agents" {
					return g.Memory
				}
			}
			return 0
		},
		Record: admission.Observed{DataDir: dataDir}.Record,
		Mode: func() admission.Mode {
			v, err := settings.Load(settings.DefaultPath())
			if err != nil {
				return admission.ModeObserve
			}
			return admission.Mode(v.AdmissionMode)
		},
		Now: time.Now,
	}
}
