package contract

const (
	MethodAgentStarted Method = "agent.started"
	MethodAgentEnded   Method = "agent.ended"
	MethodAgentStop    Method = "agent.stop"
)

// AgentID names one turn of an in-box agent. A tool call that starts one uses its own call ID, so the transcript row of that call finds its agent.
type AgentID string

// AgentReadOnlyTools is the only tool list the engine asks an in-box agent to run with: it reads files and changes nothing.
var AgentReadOnlyTools = []string{"read", "grep", "find", "ls"}

// AgentTurn asks a session's box to run one turn of an in-box agent: an agent with a conversation, a model and a tool list but no
// identity, so anything it could do it would do as the session whose box it runs in. The tool list is therefore its whole authority.
// The box runs it beside the session's own turn, and one turn per Name at a time: a second waits. It answers with agent.started and
// agent.ended.
type AgentTurn struct {
	ID           AgentID         `json:"id"`
	Name         string          `json:"name"`
	Profile      WorkflowProfile `json:"profile"`
	Tools        []string        `json:"tools"`
	Instructions string          `json:"instructions"`
	// Persist keeps the conversation between turns of the name, in the box and in the engine's transcript copy. Without it each turn is new.
	Persist bool   `json:"persist,omitempty"`
	Prompt  string `json:"prompt"`
	// FreshPrompt is what a conversation that begins with this turn reads instead of Prompt, which assumes the earlier turns. Empty keeps Prompt.
	FreshPrompt string `json:"freshPrompt,omitempty"`
	// Orient puts the parent's session file and the state of its working repository ahead of the prompt.
	Orient bool `json:"orient,omitempty"`
	// TimeoutMS is how long after it receives the turn the box ends it itself, whether or not the engine is still there to stop it. A
	// duration, not a time, so a box clock that differs from the engine's changes nothing. Zero sets none.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
	// Turn and Delivery are the parent's running turn and one of its deliveries, which file the agent's transcript under that turn.
	Turn     TurnID      `json:"turn"`
	Delivery DeliverySeq `json:"delivery"`
}

// AgentStop ends the agent's running turn. It answers with agent.ended and the text it had.
type AgentStop struct {
	ID AgentID `json:"id"`
}

// AgentStartedRequest says where the agent's turn begins in its session file, which the engine reads its steps from. Offset is
// the file's size before the turn's first record.
type AgentStartedRequest struct {
	ID         AgentID `json:"id"`
	Transcript string  `json:"transcript"`
	Offset     int64   `json:"offset"`
}
type AgentStartedResponse struct{}

// AgentEndedRequest is the agent's answer. Final is the turn's last text, or the text it had when it stopped. Fresh says the
// conversation began with this turn, whether it is new or the box lost the old one.
type AgentEndedRequest struct {
	ID      AgentID     `json:"id"`
	Final   string      `json:"final,omitempty"`
	Outcome TurnOutcome `json:"outcome"`
	Error   string      `json:"error,omitempty"`
	Usage   Usage       `json:"usage"`
	Fresh   bool        `json:"fresh,omitempty"`
}
type AgentEndedResponse struct{}

// AgentStopRequest is the owner's stop of an agent that runs for a session, by its ID.
type AgentStopRequest struct {
	Session SessionID `json:"session"`
	ID      AgentID   `json:"id"`
}
type AgentStopResponse struct{}

func (AgentTurn) BoxMessageType() BoxMessageType { return "agent.turn" }
func (AgentTurn) isBoxMessageBody()              {}
func (AgentStop) BoxMessageType() BoxMessageType { return "agent.stop" }
func (AgentStop) isBoxMessageBody()              {}
