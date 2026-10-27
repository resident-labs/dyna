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
	// MinOutputSpeed and MaxOutputSpeed bound audio.output.speed. 1 is the
	// provider default. OpenAI Realtime uses this range.
	MinOutputSpeed = 0.25
	MaxOutputSpeed = 1.5
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
	// InText adds text to the conversation. It asks for a reply only when
	// the line sets Respond.
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
	// InCommit ends the buffered user turn now and asks for one reply.
	InCommit MessageType = "commit"
)

// TurnMode says who ends a user turn.
type TurnMode string

const (
	// TurnsAuto keeps the provider's turn detection. It is the default.
	TurnsAuto TurnMode = "auto"
	// TurnsManual turns detection off. Only a client commit ends a turn.
	TurnsManual TurnMode = "manual"
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

// ToolAfter says what a tool result does to the conversation.
type ToolAfter string

const (
	// ToolAfterReply asks the model to speak about the result. It is the default.
	ToolAfterReply ToolAfter = "reply"
	// ToolAfterLater keeps the result as context and asks for no reply.
	// The real answer comes later, as Dyna Voice's dispatch does.
	ToolAfterLater ToolAfter = "later"
	// ToolAfterSilent gives the model an empty result and asks for no reply.
	// The tool has no spoken effect, as Dyna Voice's remain_silent does.
	ToolAfterSilent ToolAfter = "silent"
)

// SessionTool is a function the model may call. Each call goes to the agent.
type SessionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// After is reply, later or silent. Empty means reply.
	After ToolAfter `json:"after,omitempty"`
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
	// VADEagerness is how soon turn detection ends a turn: low, medium, high
	// or auto. Empty keeps the provider default. It conflicts with TurnsManual.
	VADEagerness string `json:"vadEagerness,omitempty"`
	// Turns is auto or manual. Empty means auto. Manual turns the provider's
	// turn detection off, so only a client commit ends a turn.
	Turns TurnMode `json:"turns,omitempty"`
	// Speed is the speaking speed, from MinOutputSpeed to MaxOutputSpeed.
	// Zero keeps the provider default.
	Speed float64 `json:"speed,omitempty"`
	// ReplyInstructions is style for every reply this session asks for,
	// including the automatic reply after turn detection and the reply after
	// a tool result. A line's own instructions replace it for that reply.
	// Empty means none.
	ReplyInstructions string `json:"replyInstructions,omitempty"`
}

// InMessage is one line from the agent.
type InMessage struct {
	Type MessageType `json:"type"`
	// Request is set on InStart.
	Request *SessionRequest `json:"request,omitempty"`
	// PCM is audio, base64 in JSON.
	PCM  []byte `json:"pcm,omitempty"`
	Text string `json:"text,omitempty"`
	// Respond, on an InText line, asks for one reply after the text is added.
	// The reply waits for speech that still plays, as say mode append does.
	// False is the default: a text line only adds context.
	Respond bool `json:"respond,omitempty"`
	// Mode is set on InSay; empty means SayAppend.
	Mode SayMode `json:"mode,omitempty"`
	// Instructions, on a say line, a text line that asks for a reply, or a
	// commit line, is style for that reply only. It replaces the session's
	// reply instructions for that reply. It does not add a conversation item.
	Instructions string `json:"instructions,omitempty"`
	CallID       string `json:"call_id,omitempty"`
	Output       string `json:"output,omitempty"`
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
