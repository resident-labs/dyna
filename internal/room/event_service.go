package room

import (
	"strconv"
)

// ServiceRole is the kind of built-in service that failed.
type ServiceRole string

// Service roles.
const (
	ServiceLibrarian ServiceRole = "librarian"
	ServiceReviewer  ServiceRole = "reviewer"
	ServiceHelper    ServiceRole = "helper"
)

// serviceRoles lists every service role the web needs as a union type.
var serviceRoles = []ServiceRole{ServiceLibrarian, ServiceReviewer, ServiceHelper}

// ServiceFailedEvent tells the root seat that a service's turn failed. The
// service posts it once per turn, so a failing loop cannot flood the feed.
type ServiceFailedEvent struct {
	Service ServiceRole `json:"service"`
	Session string      `json:"session"`
	Turn    int         `json:"turn,omitempty"`
	Error   string      `json:"error"`
	Profile string      `json:"profile,omitempty"`
	Model   string      `json:"model,omitempty"`
	// WorkerStderr is the failed worker's standard-error tail, diagnostic
	// evidence the card discloses on its own, never the cause (D-108).
	WorkerStderr string `json:"workerStderr,omitempty"`
	// Detail is the evidence the agent reads: the failure report with the
	// session and how to inspect the service. The card shows Error.
	Detail string `json:"detail,omitempty"`
}

func (ServiceFailedEvent) eventFamily() EventFamily { return FamilyService }
func (ServiceFailedEvent) eventType() EventType     { return TypeFailed }

func (s ServiceFailedEvent) eventSubject() string { return s.Session }

// eventDedupe keys one alert per service turn.
func (s ServiceFailedEvent) eventDedupe() string { return strconv.Itoa(s.Turn) }

// TypeFailed is a failed service turn. TypeHelperOutcome is an automatic
// warning about a helper turn that finished without a deliverable result.
const (
	TypeFailed        EventType = "failed"
	TypeHelperOutcome EventType = "helper-outcome"
)

// HelperOutcomeReason is why a finished helper turn needs attention.
type HelperOutcomeReason string

// Helper outcome reasons.
const (
	HelperParentUnavailable HelperOutcomeReason = "parent-unavailable"
	HelperNoArtifact        HelperOutcomeReason = "no-artifact"
)

// helperOutcomeReasons lists every reason the web needs as a union type.
var helperOutcomeReasons = []HelperOutcomeReason{HelperParentUnavailable, HelperNoArtifact}

// String renders the reason in the event's agent-facing sentence.
func (r HelperOutcomeReason) String() string {
	switch r {
	case HelperParentUnavailable:
		return "its parent is unavailable"
	case HelperNoArtifact:
		return "it produced no artifact or findings for its parent"
	default:
		return string(r)
	}
}

// HelperOutcomeEvent tells a helper's parent or the owner that a helper turn
// finished without a deliverable result. The helper is event data; the engine
// posts the event, so the warning never borrows the helper's sender.
type HelperOutcomeEvent struct {
	Reason  HelperOutcomeReason `json:"reason"`
	Session string              `json:"session"`
	Turn    int                 `json:"turn"`
	// Detail keeps the inspection command with the agent-facing event text.
	Detail string `json:"detail,omitempty"`
}

func (HelperOutcomeEvent) eventFamily() EventFamily { return FamilyService }
func (HelperOutcomeEvent) eventType() EventType     { return TypeHelperOutcome }
func (h HelperOutcomeEvent) eventSubject() string   { return h.Session }

// eventDedupe keys one warning per helper turn and reason.
func (h HelperOutcomeEvent) eventDedupe() string {
	return strconv.Itoa(h.Turn) + "\x00" + string(h.Reason)
}
