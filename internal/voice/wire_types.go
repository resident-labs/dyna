package voice

// CallStatus is the engine's voice call, as the app sees it.
type CallStatus struct {
	// Available is true once a ChatGPT account is active: the owner's opt-in
	// to voice. The app offers calls only then; Unavailable says what is
	// missing.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
	// Live is true once the call is connected. RoomID and Room name its room
	// while it starts, runs or stops.
	Live   bool   `json:"live"`
	RoomID string `json:"roomId,omitempty"`
	Room   string `json:"room,omitempty"`
	// StartedAt (unix ms) is when the call started, before any of its
	// posts. The app shows the call's voice-call posts from then on.
	StartedAt    int64  `json:"startedAt,omitempty"`
	ResumeCursor string `json:"resumeCursor,omitempty"`
	// Error says why the last call ended, when it failed.
	Error string `json:"error,omitempty"`
}
