package realtime

// PageMessage is one message from the owner's page.
type PageMessage struct {
	Type     string `json:"type"` // audio, mute, cancel, playback or hangup
	Audio    []byte `json:"audio,omitempty" tstype:"string"`
	Muted    bool   `json:"muted,omitempty"`
	ID       string `json:"id,omitempty"`        // playback: the audio item
	State    string `json:"state,omitempty"`     // playback: started or ended
	PlayedMs int64  `json:"played_ms,omitempty"` // playback ended: how much the owner heard
}

// PageEvent is one message to the owner's page.
type PageEvent struct {
	Type    string `json:"type"`            // state, audio, audio_done, clear, transcript, unspoken, tool or error
	State   string `json:"state,omitempty"` // tool: running, done or failed
	Name    string `json:"name,omitempty"`  // tool: the tool's name; Text has its arguments
	ID      string `json:"id,omitempty"`
	Audio   []byte `json:"audio,omitempty" tstype:"string"`
	Text    string `json:"text,omitempty"`
	Final   bool   `json:"final,omitempty"`
	Post    string `json:"post,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Message string `json:"message,omitempty"`
}
