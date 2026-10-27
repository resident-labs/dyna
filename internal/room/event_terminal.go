package room

// TerminalVerdict is how a terminal request ended: the owner reported it done
// or failed or declined it, a session denied it on the way up, or the request
// was withdrawn before the owner acted.
type TerminalVerdict string

// Terminal verdicts.
const (
	TerminalDone      TerminalVerdict = "done"
	TerminalFailed    TerminalVerdict = "failed"
	TerminalDeclined  TerminalVerdict = "declined"
	TerminalDenied    TerminalVerdict = "denied"
	TerminalWithdrawn TerminalVerdict = "withdrawn"
)

var terminalVerdicts = []TerminalVerdict{TerminalDone, TerminalFailed, TerminalDeclined, TerminalDenied, TerminalWithdrawn}

// TerminalExit is the exit status of the request's terminal command.
type TerminalExit struct {
	Code int `json:"code"`
}

// TerminalRequestEvent is a session asking for a command to run in a terminal.
// The request climbs the session chain and reaches the owner last, who reads
// the exact command before running it.
type TerminalRequestEvent struct {
	// RequestID is the request's short ID, such as q3.
	RequestID string `json:"requestId"`
	// Asker is the address of the session that filed the request.
	Asker string `json:"asker"`
	// Argv is empty when the request asks for a plain login shell.
	Argv   []string `json:"argv,omitempty"`
	Cwd    string   `json:"cwd"`
	Reason string   `json:"reason"`
	// Holder is the address of the session that decides next, empty when the
	// request waits for the owner.
	Holder string `json:"holder,omitempty"`
	// Detail tells a holding session what it can do.
	Detail string `json:"detail,omitempty"`
}

func (TerminalRequestEvent) eventFamily() EventFamily { return FamilyTerminal }
func (TerminalRequestEvent) eventType() EventType     { return TypeRequest }
func (t TerminalRequestEvent) eventSubject() string   { return t.RequestID }
func (t TerminalRequestEvent) eventDedupe() string    { return t.Holder }

// TerminalDoneEvent is how a terminal request ended. The asking session reads
// the verdict, the exit status, the owner's note and the shared lines; the
// owner chooses whether to share lines.
type TerminalDoneEvent struct {
	RequestID string          `json:"requestId"`
	Verdict   TerminalVerdict `json:"verdict"`
	// Exit is set only when the request's terminal command exited.
	Exit *TerminalExit `json:"exit,omitempty"`
	Note string        `json:"note,omitempty"`
	// SharedLines is the end of the screen, set only when the owner shared it.
	SharedLines []string `json:"sharedLines,omitempty"`
	// Detail is the exit status in words, for the agent text.
	Detail string `json:"detail,omitempty"`
}

func (TerminalDoneEvent) eventFamily() EventFamily { return FamilyTerminal }
func (TerminalDoneEvent) eventType() EventType     { return TypeRequestDone }
func (t TerminalDoneEvent) eventSubject() string   { return t.RequestID }
func (t TerminalDoneEvent) eventDedupe() string    { return string(t.Verdict) }
