package subagent

import (
	"time"

	harnessruntime "dyna-agent/internal/harness/runtime"
)

// QueueState is how far one queued post got toward its target session.
type QueueState string

const (
	QueueStateWaiting    QueueState = "waiting"
	QueueStateDelivering QueueState = "delivering"
	QueueStateDelivered  QueueState = "delivered"
	QueueStateForwarded  QueueState = "forwarded"
	QueueStateCancelled  QueueState = "cancelled"
	QueueStateDismissed  QueueState = "dismissed"
)

type QueueOrigin struct {
	Session         string     `json:"session"`
	Address         string     `json:"address"`
	State           QueueState `json:"state"`
	TargetDelivered bool       `json:"targetDelivered,omitempty"`
}
type QueueItem struct {
	Human       bool          `json:"human,omitempty"`
	Origins     []QueueOrigin `json:"origins,omitempty"`
	ID          string        `json:"id"`
	Text        string        `json:"text"`
	From        string        `json:"from"`
	TS          int64         `json:"ts"`
	Turn        int           `json:"turn"`
	State       QueueState    `json:"state"`
	Cancellable bool          `json:"cancellable"`
	ForwardedTo string        `json:"forwardedTo,omitempty"`
}

// TransferState is durable state for a fresh model conversation in the same
// Dyna session. The session's address, children and PR links never move.
type TransferState struct {
	// RootSeat collects a handoff for an external root without a replacement turn.
	RootSeat           bool                `json:"rootSeat,omitempty"`
	Fresh              bool                `json:"fresh,omitempty"`
	PausedFor          time.Duration       `json:"pausedFor,omitempty"`
	WaitFor            time.Duration       `json:"waitFor,omitempty"`
	ID                 string              `json:"id"`
	Session            string              `json:"session"`
	Phase              string              `json:"phase"` // waiting, stopping, starting, completed, failed, cancelled
	Profile            string              `json:"profile"`
	Model              string              `json:"model,omitempty"`
	PreviousProfile    string              `json:"previousProfile"`
	PreviousModel      string              `json:"previousModel,omitempty"`
	PreviousHarness    string              `json:"previousHarness,omitempty"`
	PreviousDir        string              `json:"previousDir"`
	PreviousResumable  bool                `json:"previousResumable"`
	PreviousRuntimePin *harnessruntime.Pin `json:"previousRuntimePin,omitempty"`
	OldTurns           int                 `json:"oldTurns"`
	Requested          time.Time           `json:"requested"`
	Deadline           time.Time           `json:"deadline"`
	StartupDeadline    time.Time           `json:"startupDeadline"`
	Ended              time.Time           `json:"ended"`
	Turn               int                 `json:"turn,omitempty"`
	PostID             string              `json:"postId"`
	Posted             bool                `json:"posted,omitempty"`
	WrittenHandoff     string              `json:"writtenHandoff,omitempty"`
	Handoff            string              `json:"handoff,omitempty"`
	Dir                string              `json:"dir,omitempty"`
	// RequestedDir is the root override from --dir. It outranks Dir, which a
	// handoff may set, so no later handoff replaces it.
	RequestedDir string `json:"requestedDir,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Source       string `json:"source,omitempty"` // cooperative, transcript, native (same Pi session) or converted
	// ConvertedSession is the target session id of a cross-harness
	// conversion. It is saved before the file is written, so a restarted
	// coordinator writes the same session again instead of a second one.
	ConvertedSession string `json:"convertedSession,omitempty"`
	// CompactionExtended records that the startup deadline was extended once
	// because the resumed Pi session reported compaction progress. The next
	// deadline is final: a resume that still produces no output falls back.
	CompactionExtended bool   `json:"compactionExtended,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Error              string `json:"error,omitempty"`
	// Notice is the one session DM that explains an automatic fallback after
	// a usage limit. Limit says why the turn left the previous profile.
	Notice       string `json:"notice,omitempty"`
	Limit        string `json:"limit,omitempty"`
	NoticePosted bool   `json:"noticePosted,omitempty"`
}

type TransferOptions struct {
	rootSeat bool

	// Fresh starts another native conversation after a completed transfer.
	// Internal lifecycle services use this for periodic context refreshes.
	Fresh bool `json:"fresh,omitempty"`

	Profile     string `json:"profile"`
	Model       string `json:"model,omitempty"`
	WaitSeconds int    `json:"waitSeconds"` // 0 means immediate transcript fallback; maximum 120
	Retry       bool   `json:"retry,omitempty"`

	// Dir is the root override for the task directory of a fresh conversation.
	// It replaces the session's directory and any saved handoff hint.
	Dir string `json:"dir,omitempty"`

	// Fallback marks an automatic move after a usage limit. Only the engine's
	// usage-limit fallback sets it: Notice is the one DM that explains the
	// move, and LimitReason says what the previous profile could not do.
	Fallback    bool   `json:"fallback,omitempty"`
	Notice      string `json:"notice,omitempty"`
	LimitReason string `json:"limitReason,omitempty"`
}

// ConsolidationState stays with the kept session. Source transcripts and task
// directories remain independent archives; consolidation never changes git.
type ConsolidationState struct {
	PausedFor     time.Duration         `json:"pausedFor,omitempty"`
	ID            string                `json:"id"`
	RoomID        string                `json:"roomId"`
	Target        string                `json:"target"`
	Sessions      []string              `json:"sessions" tstype:"string[] | null"`
	Profile       string                `json:"profile"`
	Model         string                `json:"model"`
	Phase         string                `json:"phase"`
	Requested     time.Time             `json:"requested"`
	Deadline      time.Time             `json:"deadline"`
	Ended         time.Time             `json:"ended"`
	Error         string                `json:"error,omitempty"`
	CancelPending bool                  `json:"cancelPending,omitempty"`
	Sources       []ConsolidationSource `json:"sources" tstype:"ConsolidationSource[] | null"`
	Charter       string                `json:"charter,omitempty"`
}
type ConsolidationSource struct {
	PendingIDs  []string       `json:"pendingIds,omitempty"`
	Reservation *TransferState `json:"reservation,omitempty"`
	Session     string         `json:"session"`
	Address     string         `json:"address"`
	Phase       string         `json:"phase"`
	Source      string         `json:"source,omitempty"`
	Error       string         `json:"error,omitempty"`
	Context     string         `json:"context,omitempty"`
	Dir         string         `json:"dir,omitempty"`
	// Inbox uses the original cursor, queue and delivery records. It is never
	// copied onto the target's cursor or claim map.
	Cursor int64 `json:"cursor"`
}
type ConsolidationOptions struct {
	Sessions []string `json:"sessions"`
	Target   string   `json:"target"`
	Profile  string   `json:"profile,omitempty"`
	Model    string   `json:"model,omitempty"`
}

// RoomSessionState describes how a room's sessions run. PausedAt and ResumedAt
// hold the zero time when unset because encoding/json does not omit time.Time.
type RoomSessionState struct {
	RoomID    string    `json:"roomId"`
	Paused    bool      `json:"paused"`
	PausedAt  time.Time `json:"pausedAt"`
	ResumedAt time.Time `json:"resumedAt"`
	Running   int       `json:"running"`
	Held      int       `json:"held"`
	Dormant   int       `json:"dormant"`
}

// RoomSessionKeptReason says why a room action left a session as it was. The
// web maps these named reasons to text, so it never matches an error message.
// Only RoomSessionKeptFailed is a real failure.
type RoomSessionKeptReason string

const (
	// RoomSessionKeptStartedWork means the session gained work after the list
	// was drawn. Keeping it open is the correct, neutral outcome.
	RoomSessionKeptReasonStartedWork RoomSessionKeptReason = "started-work"
	// RoomSessionKeptResponsibilities means the session still answers for work.
	RoomSessionKeptReasonResponsibilities RoomSessionKeptReason = "responsibilities"
	// RoomSessionKeptFailed means the action could not run.
	RoomSessionKeptReasonFailed RoomSessionKeptReason = "failed"
)

type RoomSessionResult struct {
	Session string                `json:"session"`
	Name    string                `json:"name,omitempty"`
	OK      bool                  `json:"ok"`
	Status  string                `json:"status"`
	Reason  RoomSessionKeptReason `json:"reason,omitempty"`
	Error   string                `json:"error,omitempty"`
}
type RoomSessionMutation struct {
	State     RoomSessionState    `json:"state"`
	Results   []RoomSessionResult `json:"results" tstype:"RoomSessionResult[] | null"`
	Succeeded int                 `json:"succeeded"`
	Kept      int                 `json:"kept"`
	Failed    int                 `json:"failed"`
}
