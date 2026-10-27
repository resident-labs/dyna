// Package agentwire is the line protocol of a live voice session for agents:
// the lines an agent sends in and reads out, and the limits that apply. The
// engine, its web server and the dyna voice session command share it, so none
// of them needs the voice provider code.
package agentwire

import (
	"encoding/json"
	"time"
)

const (
	// DefaultSessionMinutes and MaxSessionMinutes bound how long a session runs.
	DefaultSessionMinutes = 30
	MaxSessionMinutes     = 120
	// SessionIdle is how long a session waits for input audio before it ends.
	SessionIdle = 60 * time.Second
	// DefaultSessionRate is the sample rate of the agent's audio, in hertz.
	DefaultSessionRate = 24000
	// MinSessionRate and MaxSessionRate bound the agent's sample rate.
	MinSessionRate = 8000
	MaxSessionRate = 96000
	// MaxImageBytes is the largest image one image line may carry.
	MaxImageBytes = 5 << 20
)

// MessageType names a line of the session protocol, in either direction.
type MessageType string

// Lines the agent sends in.
const (
	// InStart opens the session. It is the first line, and only the CLI sends it.
	InStart MessageType = "start"
	// InEnd tells the engine that stdin ended. Only the command sends it, so
	// the engine can still send its last lines.
	InEnd MessageType = "end"
	// InAudio carries microphone PCM16 mono at the session's rate.
	InAudio MessageType = "audio"
	// InText adds text to the conversation without asking for a reply.
	InText MessageType = "text"
	// InSay asks the voice to speak the text. Its Mode says what to do with
	// speech that still plays.
	InSay MessageType = "say"
	// InToolResult answers a tool call.
	InToolResult MessageType = "tool_result"
	// InInterrupt stops the reply that is playing.
	InInterrupt MessageType = "interrupt"
	// InImage adds a PNG or JPEG image to the conversation without a reply.
	InImage MessageType = "image"
)

// SayMode says what an InSay line does when an earlier reply still plays.
type SayMode string

const (
	// SayAppend waits for the reply that plays, then speaks. It is the default.
	SayAppend SayMode = "append"
	// SayReplace cancels the reply that plays and speaks the new text now. The
	// engine sends speech_started so the agent drops audio it has buffered, and
	// sends no more audio of the cancelled reply.
	SayReplace SayMode = "replace"
)

// Lines the engine sends out.
const (
	OutReady        MessageType = "ready"
	OutAudio        MessageType = "audio"
	OutSpeechStart  MessageType = "speech_started"
	OutTranscript   MessageType = "transcript"
	OutToolCall     MessageType = "tool_call"
	OutResponseDone MessageType = "response_done"
	OutError        MessageType = "error"
	OutClosed       MessageType = "closed"
)

// SessionTool is a function the model may call. Each call goes to the agent.
type SessionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// SessionRequest opens a session. Zero values use the defaults: no extra
// instructions, the provider's voice, the voice settings' provider, 24 kHz
// audio and 30 minutes.
type SessionRequest struct {
	Instructions string        `json:"instructions,omitempty"`
	Voice        string        `json:"voice,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	Tools        []SessionTool `json:"tools,omitempty"`
	Rate         int           `json:"rate,omitempty"`
	MaxMinutes   int           `json:"maxMinutes,omitempty"`
}

// InMessage is one line from the agent.
type InMessage struct {
	Type MessageType `json:"type"`
	// Request is set on InStart.
	Request *SessionRequest `json:"request,omitempty"`
	// PCM is audio, base64 in JSON.
	PCM  []byte `json:"pcm,omitempty"`
	Text string `json:"text,omitempty"`
	// Mode is set on InSay; empty means SayAppend.
	Mode   SayMode `json:"mode,omitempty"`
	CallID string  `json:"call_id,omitempty"`
	Output string  `json:"output,omitempty"`
	// Image and Mime are the file's bytes and type on InImage.
	Image []byte `json:"image,omitempty"`
	Mime  string `json:"mime,omitempty"`
}

// TranscriptRole says who spoke a transcript line.
type TranscriptRole string

// Transcript roles.
const (
	RoleUser      TranscriptRole = "user"
	RoleAssistant TranscriptRole = "assistant"
)

// OutMessage is one line to the agent.
type OutMessage struct {
	Type       MessageType     `json:"type"`
	Rate       int             `json:"rate,omitempty"`
	ResponseID string          `json:"response_id,omitempty"`
	PCM        []byte          `json:"pcm,omitempty"`
	Role       TranscriptRole  `json:"role,omitempty"`
	Text       string          `json:"text,omitempty"`
	CallID     string          `json:"call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Message    string          `json:"message,omitempty"`
	Reason     EndReason       `json:"reason,omitempty"`
}

// EndReason is why a session ended.
type EndReason string

// End reasons.
const (
	EndInputClosed    EndReason = "input-closed"
	EndTimeLimit      EndReason = "time-limit"
	EndIdle           EndReason = "idle"
	EndProviderClosed EndReason = "provider-closed"
	EndError          EndReason = "error"
	EndCancelled      EndReason = "cancelled"
)
