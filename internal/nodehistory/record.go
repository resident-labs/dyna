// Package nodehistory stores remote probe readings in the doctor's sample table.
package nodehistory

import (
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/sysload"
)

// Recorder writes successful remote probes under their stable node IDs.
func Recorder(dataDir string) func(machines.Machine, machines.ProbeResult) error {
	samples := doctor.Samples{DataDir: dataDir}
	return func(machine machines.Machine, result machines.ProbeResult) error {
		return Record(samples, machine, result)
	}
}

// Record writes one successful remote probe under its stable node ID.
func Record(samples doctor.Samples, machine machines.Machine, result machines.ProbeResult) error {
	l := result.Load
	used := l.MemTotal - min(l.MemAvailable, l.MemTotal)
	cores := 0.0
	if l.CPUPercent != nil {
		cores = *l.CPUPercent * float64(l.CPUs) / 100
	}
	return samples.Record(machine.ID, sysload.Load{
		At: result.At, CPUs: l.CPUs, Load: l.LoadAvg, CPUPercent: l.CPUPercent,
		Memory: sysload.Memory{Total: l.MemTotal, Used: used},
		Groups: []sysload.Group{{Name: "machine", Cores: cores, Memory: used}},
	})
}
