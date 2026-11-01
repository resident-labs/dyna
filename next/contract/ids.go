package contract

type (
	RoomID         string // "room_<ulid>"
	SessionID      string // "ses_<ulid>"
	BoxID          string // "box_<ulid>"
	EventID        string // "ev_<ulid>"
	OpID           string // "op_<ulid>"
	QuestionID     string // "q_<ulid>"
	ShellJobID     string // a box shell job, such as "job_1f3a9c0b7d42"
	GrantID        string // "gr_<ulid>"
	TurnID         string // "turn_<ulid>"
	DeviceID       string // "dev_<ulid>", a paired device
	SubscriptionID string // "sub_<ulid>"
	GoalID         string // "g<n>"; v1 numbers are kept at import (M3)
	WorkflowRunID  string // "wf_<ulid>"
	HostName       string // a registered host, such as "hermeslab"
	RepoName       string // "owner/name" on GitHub
	ProfileName    string // a registered profile, such as "terra"
	DurationMS     int64  // a duration in milliseconds
)

// ControlPlane is the reserved host name of the engine's own machine.
const ControlPlane HostName = "control-plane"

// AllHosts in a grant matches every registered host and the control plane.
const AllHosts HostName = "*"

type PrincipalKind string

const (
	PrincipalOwner   PrincipalKind = "owner"   // the human
	PrincipalSession PrincipalKind = "session" // an agent session; root is one
	PrincipalEngine  PrincipalKind = "engine"  // the engine as its own sender
)

// Principal is who acts or who receives. The engine sets it from the
// connection, never from a request body.
type Principal struct {
	Kind    PrincipalKind `json:"kind"`
	Session SessionID     `json:"session,omitempty"` // Kind session only
	Device  DeviceID      `json:"device,omitempty"`  // Paired owner device, if any
}
