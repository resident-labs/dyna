package room

// ApprovalClass says who may decide an approval request.
type ApprovalClass string

// Approval classes.
const (
	ApprovalClassAgent ApprovalClass = "agent"
	ApprovalClassOwner ApprovalClass = "owner"
)

// ApprovalAction is one decision on an approval request.
type ApprovalAction string

// Approval actions.
const (
	ApprovalApprove ApprovalAction = "approve"
	ApprovalDeny    ApprovalAction = "deny"
	ApprovalPass    ApprovalAction = "pass"
	ApprovalAuto    ApprovalAction = "auto"
)

// ApprovalState is the final state of an approval request.
type ApprovalState string

// Final approval states.
const (
	ApprovalDone      ApprovalState = "done"
	ApprovalFailed    ApprovalState = "failed"
	ApprovalDenied    ApprovalState = "denied"
	ApprovalExpired   ApprovalState = "expired"
	ApprovalCancelled ApprovalState = "cancelled"
)

// ApprovalRequestedEvent describes the exact write that waits for a decision.
// Title and Body carry the full final text, including any attribution line.
type ApprovalRequestedEvent struct {
	RequestID  string        `json:"requestId"`
	Kind       string        `json:"kind"`
	Class      ApprovalClass `json:"class"`
	Requester  string        `json:"requester"`
	At         string        `json:"at"`
	Repository string        `json:"repository,omitempty"`
	Server     string        `json:"server,omitempty"`
	Command    string        `json:"command,omitempty"`
	Tool       string        `json:"tool,omitempty"`
	Title      string        `json:"title,omitempty"`
	Body       string        `json:"body,omitempty"`
	// Detail names the commands that decide the request, for the agent text.
	Detail string `json:"detail,omitempty"`
}

func (ApprovalRequestedEvent) eventFamily() EventFamily { return FamilyApproval }
func (ApprovalRequestedEvent) eventType() EventType     { return TypeApprovalRequested }
func (e ApprovalRequestedEvent) eventSubject() string   { return e.RequestID }
func (e ApprovalRequestedEvent) eventDedupe() string    { return e.At }

// ApprovalDecidedEvent records a decision. At is the new holder after a pass.
type ApprovalDecidedEvent struct {
	RequestID string         `json:"requestId"`
	Actor     string         `json:"actor"`
	Action    ApprovalAction `json:"action"`
	Reason    string         `json:"reason,omitempty"`
	At        string         `json:"at,omitempty"`
}

func (ApprovalDecidedEvent) eventFamily() EventFamily { return FamilyApproval }
func (ApprovalDecidedEvent) eventType() EventType     { return TypeApprovalDecided }
func (e ApprovalDecidedEvent) eventSubject() string   { return e.RequestID }
func (e ApprovalDecidedEvent) eventDedupe() string {
	return string(e.Action) + "\x00" + e.Actor + "\x00" + e.At
}

// ApprovalResultEvent tells the requester how its held write ended.
type ApprovalResultEvent struct {
	RequestID string        `json:"requestId"`
	State     ApprovalState `json:"state"`
	Actor     string        `json:"actor,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	ExitCode  *int          `json:"exitCode,omitempty"`
	URL       string        `json:"url,omitempty"`
	Output    []string      `json:"output,omitempty"`
}

func (ApprovalResultEvent) eventFamily() EventFamily { return FamilyApproval }
func (ApprovalResultEvent) eventType() EventType     { return TypeApprovalResult }
func (e ApprovalResultEvent) eventSubject() string   { return e.RequestID }
func (e ApprovalResultEvent) eventDedupe() string    { return string(e.State) }
