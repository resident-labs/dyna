package room

// ShellState is how a detached shell job ended: with its command's exit
// status, or stopped before it finished.
type ShellState string

// Shell states.
const (
	ShellCompleted ShellState = "completed"
	ShellFailed    ShellState = "failed"
	ShellStopped   ShellState = "stopped"
)

// shellStates lists every shell state the web needs as a union type.
var shellStates = []ShellState{ShellCompleted, ShellFailed, ShellStopped}

// ShellEvent is the result of a detached `dyna shell` job: the facts its row
// shows, so the row never reads the post text. The job ID stays opaque: it is
// the row's own key and the way to the stored output, never a label.
type ShellEvent struct {
	// ShellID is the shell store's job ID.
	ShellID string `json:"shellId"`
	// Label is the human label of the command: its first word, and the short
	// description its caller gave.
	Label string     `json:"label"`
	State ShellState `json:"state"`
	// ExitCode is the command's exit status, -1 when a stop ended the job
	// before it finished.
	ExitCode   int `json:"exitCode,omitempty"`
	DurationMS int `json:"durationMs,omitempty"`
	// Output is the last lines of the job's output. The row shows them behind
	// its own toggle only, never in the room feed it is hidden from.
	Output []string `json:"output,omitempty"`
	// Parent is the session that started the job, empty when the owner did.
	Parent string `json:"parent,omitempty"`
	// Detail is the rest of the line an agent reads, when the state and the
	// exit code do not say it: where the full output lives.
	Detail string `json:"detail,omitempty"`
}

func (ShellEvent) eventFamily() EventFamily { return FamilyShell }
func (ShellEvent) eventType() EventType     { return TypeResult }
func (s ShellEvent) eventSubject() string   { return s.ShellID }

// eventDedupe keys one result per job, so a second notice of the same job
// posts nothing.
func (s ShellEvent) eventDedupe() string { return s.ShellID }

// ShellSilenceEvent reports an unchanged output log, not a deadlock.
type ShellSilenceEvent struct {
	ShellID string `json:"shellId"`
	Label   string `json:"label"`
	// Session is the name of the session that started the job, or the owner
	// when no session did.
	Session string `json:"session"`
	Minutes int    `json:"minutes"`
}

func (ShellSilenceEvent) eventFamily() EventFamily { return FamilyShell }
func (ShellSilenceEvent) eventType() EventType     { return TypeShellSilence }
func (s ShellSilenceEvent) eventSubject() string   { return s.ShellID }
func (s ShellSilenceEvent) eventDedupe() string    { return s.ShellID }
