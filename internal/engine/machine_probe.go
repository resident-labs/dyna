package engine

import (
	"context"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/sysload"
)

// machineProbeEvery is the slow timer that probes the registered machines, so
// a machine that was off during the last probe becomes online again without a
// change to its row. A registry change wakes the same worker at once.
const machineProbeEvery = 5 * time.Minute

// probeMachines probes the machines and reports when to probe again. This
// machine's own row takes its load from the engine's sampler, so the local
// probe needs no shell sample. A CI machine below the disk floor is degraded
// and holds its CI work, and root hears about it once.
func probeMachines(ctx context.Context, load *sysload.Sampler, alerter machines.Alerter) (time.Time, error) {
	prober := machines.Prober{DiskFloorBytes: ciDiskFloorBytes(), Alerter: alerter}
	if load != nil {
		prober.Load = load.Load
	}
	return time.Now().Add(machineProbeEvery), prober.ProbeAll(ctx)
}

// ciDiskFloorBytes is the free space a CI machine must keep, as settings say it.
// A settings file that cannot be read keeps the default floor, so a broken file
// never starts CI on a full machine.
func ciDiskFloorBytes() uint64 {
	values, err := settings.Load("")
	if err != nil {
		return settings.Defaults().CIDiskFloorBytes()
	}
	return values.CIDiskFloorBytes()
}

// ciDiskFloor is ciDiskFloorBytes in bytes, as a reader of a stored figure
// takes it.
func ciDiskFloor() int64 {
	return int64(ciDiskFloorBytes())
}
