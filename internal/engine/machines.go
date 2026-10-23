package engine

import (
	"context"
	"math"
	"sort"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/websvc"
)

// machineAPI answers the machine calls. The registry gives every row; the live
// figures come from this engine's own machine.
func (s services) machineAPI() websvc.MachineAPI {
	return websvc.MachineAPI{Store: machines.Store{}, Version: callspec.Build, Live: s.machineLive}
}

// machineLive reads what this machine uses now: its CPU share, the sessions
// that run on it, its live computers and the harnesses installed here. A
// figure the engine cannot read stays nil, so the client shows no number.
func (s services) machineLive(ctx context.Context) websvc.MachineLive {
	out := websvc.MachineLive{}
	if s.load != nil {
		if l := s.load.Load(); l.CPUPercent != nil {
			cpu := int(math.Round(*l.CPUPercent))
			out.CPU = &cpu
		}
	}
	if s.rooms != nil {
		if list, err := s.rooms.Rooms(); err == nil {
			agents := 0
			for _, r := range list {
				agents += r.Running
			}
			out.Agents = &agents
		}
	}
	if s.computers != nil {
		if n, err := s.computers.Live(ctx); err == nil {
			out.Computers = &n
		}
	}
	if s.runtime != nil && s.runtime.Bundles != nil {
		if bundles, err := s.runtime.Bundles.List(); err == nil {
			for _, b := range bundles {
				out.Harnesses = append(out.Harnesses, websvc.MachineHarness{Name: b.Harness, Version: b.Version})
			}
			sort.Slice(out.Harnesses, func(i, j int) bool { return out.Harnesses[i].Name < out.Harnesses[j].Name })
		}
	}
	return out
}
