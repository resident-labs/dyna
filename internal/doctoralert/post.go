package doctoralert

import (
	"fmt"

	"dyna-agent/internal/room"
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
			event.Findings = append(event.Findings, room.AlertFinding{Severity: severity, Title: f.Cause, Cause: f.Evidence})
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
