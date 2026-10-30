package room

// HookState is how a hook run stands. A running run has just started, and the
// other states end it.
type HookState string

// Hook run states.
const (
	HookRunning HookState = "running"
	HookPassed  HookState = "passed"
	HookFailed  HookState = "failed"
	HookError   HookState = "error"
)

// hookStates lists every state the web needs as a union type.
var hookStates = []HookState{HookRunning, HookPassed, HookFailed, HookError}

// HookEvent tells a room that a hook run started and how it ended. The two
// types share the payload: a running state is the start, every other state is
// the result. One card per run shows the start folding into the result.
//
// Every start is followed by its own result, so a card never stays running.
// A start posts only where the run already has a room (the event's room or the
// repo's), and a routine run never creates one. A failing alert hook may
// create the repo's room, so the first failure of a repo nobody uses as a room
// shows its result without the start that came before it.
type HookEvent struct {
	// RunID names the stored run. The card opens the run's log with it, so no
	// filesystem path reaches the web.
	RunID string `json:"runId"`
	// Repo is the main checkout whose hook ran, which the log read needs too.
	Repo string `json:"repo"`
	Hook string `json:"hook"`
	// Event is the repo event that started the run.
	Event string `json:"event"`
	// Label is the hook's own label, when it has one.
	Label string `json:"label,omitempty"`
	// Command is the first word of the command only: the full command can
	// hold a secret, so it never leaves the hooks store. A built-in run has
	// no command, because the engine runs its own pass.
	Command string    `json:"command,omitempty"`
	State   HookState `json:"state"`
	// Builtin marks a run of an automatic hook, so the card names the pass
	// instead of a command.
	Builtin bool `json:"builtin,omitempty"`
	// DurationMS is how long the run took. A start has none.
	DurationMS int `json:"durationMs,omitempty"`
	// Detail is the cause the state cannot carry alone, such as the exit code,
	// or a checkout warning from an install unit.
	Detail string `json:"detail,omitempty"`
}

func (HookEvent) eventFamily() EventFamily { return FamilyHook }

// eventType reads the state: a running run posts the start, any other state
// posts the result.
func (h HookEvent) eventType() EventType {
	if h.State == HookRunning {
		return TypeStarted
	}
	return TypeResult
}

func (h HookEvent) eventSubject() string { return h.RunID }
func (h HookEvent) eventThread() string {
	if h.RunID == "" {
		return ""
	}
	return "hook:" + h.RunID
}

// eventDedupe keys one post per run and type, so a start and its result are two
// posts that the web folds into one card.
func (h HookEvent) eventDedupe() string {
	return h.RunID + "\x00" + string(h.eventType())
}
