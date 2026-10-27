package room

// VoiceSessionState is the end of a live agent voice session that a notice
// reports.
type VoiceSessionState string

// Voice session states.
const (
	VoiceStarted VoiceSessionState = "started"
	VoiceEnded   VoiceSessionState = "ended"
)

// VoiceEndReason is why a live agent voice session ended.
type VoiceEndReason string

// Voice end reasons.
const (
	// VoiceEndInput: the agent closed its input.
	VoiceEndInput VoiceEndReason = "input-closed"
	// VoiceEndTimeLimit: the session ran as long as its limit allows.
	VoiceEndTimeLimit VoiceEndReason = "time-limit"
	// VoiceEndIdle: no input audio came for the idle limit.
	VoiceEndIdle VoiceEndReason = "idle"
	// VoiceEndProvider: the voice provider closed the connection.
	VoiceEndProvider VoiceEndReason = "provider-closed"
	// VoiceEndError: the session failed.
	VoiceEndError VoiceEndReason = "error"
	// VoiceEndCancelled: the engine or the caller's connection ended it.
	VoiceEndCancelled VoiceEndReason = "cancelled"
)

var (
	voiceSessionStates = []VoiceSessionState{VoiceStarted, VoiceEnded}
	voiceEndReasons    = []VoiceEndReason{VoiceEndInput, VoiceEndTimeLimit, VoiceEndIdle, VoiceEndProvider, VoiceEndError, VoiceEndCancelled}
)

// VoiceSessionEvent tells a room that an agent's live voice session started
// or ended. It names the session that spent the provider's quota, the
// provider, and for an end the time used and why it ended.
type VoiceSessionEvent struct {
	Type VoiceSessionState `json:"type"`
	// Voice is the voice session's own ID, which pairs a start with its end.
	Voice string `json:"voice"`
	// Session is the address of the session that holds the voice session.
	Session  string `json:"session"`
	Provider string `json:"provider"`
	// Seconds is the time the session ran. It is set on an end.
	Seconds int            `json:"seconds,omitempty"`
	Reason  VoiceEndReason `json:"reason,omitempty"`
}

func (VoiceSessionEvent) eventFamily() EventFamily { return FamilyVoice }
func (v VoiceSessionEvent) eventType() EventType   { return EventType(v.Type) }

// eventSubject is the voice session, so its start and end fold into one row.
func (v VoiceSessionEvent) eventSubject() string { return v.Voice }

// eventDedupe keys one post per voice session and state.
func (v VoiceSessionEvent) eventDedupe() string { return string(v.Type) + "\x00" + v.Voice }
