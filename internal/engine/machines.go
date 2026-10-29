package engine

import (
	"context"
	"math"
	"sort"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// machineAPI answers the machine calls. The registry gives every row; the live
// figures come from this engine's own machine.
func (s services) machineAPI() websvc.MachineAPI {
	var alerter machines.Alerter
	if s.rooms != nil && s.rooms.Notify != nil {
		alerter = machineDiskAlerter{notify: s.rooms.Notify}
	}
	return websvc.MachineAPI{Store: machines.Store{}, Version: callspec.Build, Live: s.machineLive, Running: s.machineRunning, CI: s.machineCI, CIQueue: s.machineCIQueue, DiskFloor: ciDiskFloor, Alerter: alerter, RoomName: s.machineRoomName}
}

// machineRoomName names the room whose root allowed host checks, so the Machine
// page says which room it was. A room the registry no longer holds keeps its ID.
func (s services) machineRoomName(roomID string) string {
	if roomID == "" || s.rooms == nil || s.rooms.Notify == nil || s.rooms.Notify.Rooms == nil {
		return roomID
	}
	rec, err := s.rooms.Notify.Rooms.Resolve(roomID)
	if err != nil || rec.Name == "" {
		return roomID
	}
	return rec.Name
}

// machineCI lists the local CI runs in flight on every machine. The runs live
// in the engine's workers, so the PR view reads them from the scheduler's own
// set and names the PR for the page.
func (s services) machineCI() []websvc.MachineCIRun {
	if s.rooms == nil || s.rooms.PRs == nil {
		return nil
	}
	runs := s.rooms.PRs.RunningCI()
	out := make([]websvc.MachineCIRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, websvc.MachineCIRun{
			Machine: run.Machine, Number: run.Number, Kind: machineCIRunKind(run.Kind), Branch: run.Branch,
			Title: run.Title, URL: run.URL, Trial: run.Trial, Suites: run.Suites, Started: run.Started,
		})
	}
	return out
}

// machineCIQueue reads one node's shared queue: the checks and jobs it runs
// now and the work that waits for room on it.
func (s services) machineCIQueue(name string) (int, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	running, waiting, err := (jobs.Admission{}).Snapshot(ctx, name)
	if err != nil {
		return 0, 0
	}
	return running, waiting
}

// machineCIRunKind names the page's kind for a run. An unknown kind reads as a
// pull request, which is the only kind this engine made before local heads and
// syncs reached the machine view.
func machineCIRunKind(kind watch.RunningCIKind) websvc.MachineCIRunKind {
	switch kind {
	case watch.RunningCILocalChange:
		return websvc.MachineCIRunLocalChange
	case watch.RunningCISync:
		return websvc.MachineCIRunSync
	}
	return websvc.MachineCIRunPullRequest
}

// machineRunning lists the sessions that run on this machine now: every open
// session with a live or queued turn. The room service names it the way the
// room lists name it, so the page can open each one.
func (s services) machineRunning() []websvc.MachineAgent {
	if s.rooms == nil || s.rooms.Notify == nil || s.rooms.Notify.Sessions == nil || s.rooms.Notify.Sessions.Store == nil {
		return nil
	}
	list, err := s.rooms.Notify.Sessions.Store.List(false)
	if err != nil {
		return nil
	}
	out := []websvc.MachineAgent{}
	for _, info := range list {
		if info.Status != subagent.StatusRunning && info.Status != subagent.StatusQueued {
			continue
		}
		owner := s.rooms.LoadOwner(info.ID)
		out = append(out, websvc.MachineAgent{ID: owner.ID, Name: owner.Name, Address: owner.Address, Room: owner.Room, RoomID: owner.RoomID, Hue: owner.Hue})
	}
	return out
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
			// A paused or archived room runs nothing, the same rule the room
			// rail's working count used.
			agents := 0
			for _, r := range list {
				if r.Archived || r.Paused {
					continue
				}
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
