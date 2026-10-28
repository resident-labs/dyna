package room

// AlertSource is what observed the condition an alert reports.
type AlertSource string

// Alert sources.
const (
	// AlertDoctor is a doctor report.
	AlertDoctor AlertSource = "doctor"
	// AlertEngine is the engine itself, as when it stays read-only.
	AlertEngine AlertSource = "engine"
	// AlertConsolidation is a session consolidation that paused with an error.
	AlertConsolidation AlertSource = "consolidation"
	// AlertShell reports processes that survived a shell stop.
	AlertShell AlertSource = "shell"
)

// alertSources lists every alert source the web needs as a union type.
var alertSources = []AlertSource{AlertDoctor, AlertEngine, AlertConsolidation, AlertShell}

// AlertSeverity is how urgent one finding of an alert is. A doctor ranks its
// causes, so its first finding is the one to act on now.
type AlertSeverity string

// Alert severities.
const (
	// AlertWarning is a cause the owner should fix soon.
	AlertWarning AlertSeverity = "warning"
	// AlertCritical is the cause that needs the owner now.
	AlertCritical AlertSeverity = "critical"
)

// alertSeverities lists every severity the web needs as a union type.
var alertSeverities = []AlertSeverity{AlertWarning, AlertCritical}

// AlertFinding is one cause an alert names, with the numbers behind it.
type AlertFinding struct {
	Severity AlertSeverity `json:"severity"`
	// Title states the cause in a few words, such as "machine is overloaded".
	Title string `json:"title"`
	// Cause is the evidence the title rests on, with its numbers.
	Cause string `json:"cause,omitempty"`
}

// AlertEvent tells the root seat that something needs the owner now: a doctor
// report, or the engine stuck read-only.
type AlertEvent struct {
	Source AlertSource `json:"source"`
	// ReportID names the doctor report in the machine's report list.
	ReportID string `json:"reportId,omitempty"`
	// Trigger names what started the check, such as "hook main-updated".
	Trigger string `json:"trigger,omitempty"`
	// Summary is the one line the alert states.
	Summary string `json:"summary,omitempty"`
	// Findings are the causes the doctor ranked, worst first.
	Findings []AlertFinding `json:"findings,omitempty"`
	// NextCommand is the one command the owner can run about the first doctor
	// finding, as a chip the card copies.
	NextCommand string `json:"nextCommand,omitempty"`
	// Remedy is the one command that fixes what an engine alert reports, as a
	// chip the card copies.
	Remedy string `json:"remedy,omitempty"`
	// Detail is the evidence the agent reads: the report text or the
	// migration failure.
	Detail string `json:"detail,omitempty"`
}

func (AlertEvent) eventFamily() EventFamily { return FamilyAlert }

// eventType reads the source: one alert type per observer.
func (a AlertEvent) eventType() EventType { return EventType(a.Source) }

func (a AlertEvent) eventSubject() string {
	if a.Source == AlertDoctor {
		return a.ReportID
	}
	return string(a.Source)
}

// eventDedupe keys one alert per report, and one per failure otherwise.
func (a AlertEvent) eventDedupe() string {
	if a.Source == AlertDoctor {
		return a.ReportID
	}
	return a.Detail
}
