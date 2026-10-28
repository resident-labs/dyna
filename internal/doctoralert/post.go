package doctoralert

import (
	"fmt"
	"strings"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// PostNote sends a note to the root seat of its room as one typed doctor alert.
// An overload wakes the root; a recovery only waits for the root's next turn.
func PostNote(rooms *room.Store, n Note) error {
	rec, err := rooms.Resolve(n.Room.RoomID)
	if err != nil {
		return err
	}
	event := room.AlertEvent{Source: room.AlertDoctor, Trigger: "hook schedule", Summary: n.Report.Summary, Detail: n.Advice()}
	if n.Recovered {
		event.Summary = n.Machine + " is healthy again"
		event.ReportID = fmt.Sprintf("recovered-%s-%s", n.Machine, rec.ID)
	} else {
		event.ReportID = fmt.Sprint(n.Report.ID)
		event.Detail += "\n\n" + n.Report.Text()
		for i, f := range n.Report.Findings {
			severity := room.AlertWarning
			if i == 0 {
				severity = room.AlertCritical
			}
			cause := f.Evidence
			if f.Kind == doctor.GroupOutside && len(n.Outside) > 0 {
				lines := make([]string, len(n.Outside))
				for j, o := range n.Outside {
					lines[j] = o.Text()
				}
				cause += ". The biggest programs outside Dyna: " + strings.Join(lines, "; ")
			}
			event.Findings = append(event.Findings, room.AlertFinding{Severity: severity, Title: f.Cause, Cause: cause})
			if event.NextCommand == "" {
				event.NextCommand = f.Command
			}
		}
	}
	_, err = rooms.PostEventInput(room.PostEventInput{
		RoomID:   rec.ID,
		Event:    event,
		FreshID:  true,
		Producer: room.ProducerDoctor,
		To:       []string{rec.Name + ":" + room.RootAgent},
		NoWake:   n.Recovered,
	})
	return err
}

// PostCapNote sends one cap note to the root seat of its room, as one typed
// machine event the owner also reads. The root wakes: it can rein the room's
// work in before the kernel throttles its memory or refuses its forks.
func PostCapNote(rooms *room.Store, n CapNote) error {
	rec, err := rooms.Resolve(n.RoomID)
	if err != nil {
		return err
	}
	trees := func(list []doctor.CapTree) []room.RoomCapTree {
		out := make([]room.RoomCapTree, len(list))
		for i, t := range list {
			out[i] = room.RoomCapTree{Command: t.Command, PID: t.PID, Memory: int(t.Memory), Count: t.Count}
		}
		return out
	}
	event := room.RoomCapEvent{
		Room:        rec.Name,
		Cap:         room.RoomCapKind(n.Cap),
		Current:     int(n.Current),
		Limit:       int(n.Limit),
		Summary:     capSummary(n.Cap, n.Current, n.Limit),
		TopByMemory: trees(n.Trees.ByMemory),
		TopByCount:  trees(n.Trees.ByCount),
		Detail:      treeDetail(n.Trees),
		AtMS:        int(n.At.UnixMilli()),
	}
	_, err = rooms.PostEventInput(room.PostEventInput{
		RoomID: rec.ID,
		Event:  event,
		To:     []string{rec.Name + ":" + room.RootAgent},
	})
	return err
}

// capSummary is the facts line of one cap: binary units for memory, plain
// counts for processes.
func capSummary(cap doctor.Cap, current, limit uint64) string {
	if cap == doctor.CapPidsMax {
		return fmt.Sprintf("%d of %d processes", current, limit)
	}
	return fmt.Sprintf("%s of %s", settings.FormatBytes(int64(current)), settings.FormatBytes(int64(limit)))
}

// treeDetail is the room's biggest process trees as the root reads them,
// with the memory order first.
func treeDetail(trees doctor.CapTrees) string {
	lines := func(list []doctor.CapTree, label string) string {
		if len(list) == 0 {
			return ""
		}
		out := make([]string, len(list))
		for i, t := range list {
			out[i] = fmt.Sprintf("%s (pid %d): %s, %d processes", t.Command, t.PID, settings.FormatBytes(int64(t.Memory)), t.Count)
		}
		return label + "\n" + strings.Join(out, "\n")
	}
	detail := lines(trees.ByMemory, "Biggest by memory:")
	if byCount := lines(trees.ByCount, "Most processes:"); byCount != "" {
		if detail != "" {
			detail += "\n\n"
		}
		detail += byCount
	}
	return detail
}
