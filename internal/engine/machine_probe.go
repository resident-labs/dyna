package engine

import (
	"context"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/nodehistory"
	"dyna-agent/internal/store"
	"dyna-agent/internal/sysload"
)

// machineProbeEvery is the slow timer that probes the registered machines, so
// a machine that was off during the last probe becomes online again without a
// change to its row. A registry change wakes the same worker at once.
const machineProbeEvery = 5 * time.Minute

// probeMachines probes the machines and reports when to probe again. This
// machine's own row takes its load from the engine's sampler, so the local
// probe needs no shell sample. A node whose disk starts to block new work
// alerts root once, through the alerter.
func probeMachines(ctx context.Context, load *sysload.Sampler, alerter machines.Alerter) (time.Time, error) {
	prober := machines.Prober{Alerter: alerter, Record: nodehistory.Recorder(store.DataDir())}
	if load != nil {
		prober.Load = load.Load
	}
	return time.Now().Add(machineProbeEvery), prober.ProbeAll(ctx)
}
