package doctoralert

import (
	"strconv"

	"dyna-agent/internal/doctor"
	"dyna-agent/internal/room"
)

// AlertEvent converts a doctor report into the common typed alert. Delivery
// paths add their own advice and recovery state.
func AlertEvent(r doctor.Report) room.AlertEvent {
	findings := make([]room.AlertFinding, len(r.Findings))
	next := ""
	for i, f := range r.Findings {
		severity := room.AlertWarning
		if i == 0 {
			severity = room.AlertCritical
		}
		findings[i] = room.AlertFinding{Severity: severity, Title: f.Cause, Cause: f.Evidence}
		if next == "" && f.Command != "" {
			next = f.Command
		}
	}
	return room.AlertEvent{
		Source:      room.AlertDoctor,
		ReportID:    strconv.FormatInt(r.ID, 10),
		Trigger:     r.Trigger,
		Summary:     r.Summary,
		Findings:    findings,
		NextCommand: next,
		Detail:      r.Text(),
	}
}
