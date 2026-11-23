// Package tools defines the agent and CLI surface as one method table: every
// engine method with its scope rule, who may call it, and the agent actions and
// CLI commands bound to it. The table is the engine gate's authorization
// source; deeper checks, such as room pinning and grants, live in the engine
// services.
package tools

import (
	"reflect"

	"github.com/resident-labs/dyna/contract"
)

// Access says who may call a method; the engine derives the caller from its socket.
type Access uint8

const (
	OwnerAccess      Access = iota + 1 // the owner only
	UserAccess                         // the owner or a session
	BoxAccess                          // a box socket only
	OwnerLocalAccess                   // the owner, not through the web
)

// BoxHandler names an action the box agent serves itself, without the engine.
type BoxHandler uint8

const (
	NoBoxHandler BoxHandler = iota
	VisualCheck
	ComputerOpen
	ComputerClick
	ComputerType
	ComputerText
	ComputerScreenshot
	ComputerMove
	ComputerDrag
	ComputerScroll
	ComputerKey
	ComputerWait
	ComputerLaunch
	ComputerStop
	ComputerCheck
	lastBoxHandler = ComputerCheck
)

// Action is one agent action with its CLI commands. Request is the schema
// source: the engine request, or the operation arguments when Op is set.
type Action struct {
	Tool, Name string
	Request    reflect.Type
	Op         contract.OpKind
	Help       string
	CLI        []string
}

// BoxAction is an action the box agent handles locally.
type BoxAction struct {
	Tool, Name string
	Handler    BoxHandler
	Request    reflect.Type
	Help       string
	CLI        []string
}

// MethodEntry is the one authorization row for a method. Scope is empty when
// the handler checks its own scope: op.submit by operation kind, and
// device.scopes, which answers every owner device so a client can learn what it holds.
type MethodEntry struct {
	Method  contract.Method
	Scope   contract.Scope
	Access  Access
	Actions []Action
}

// PostInput is what a caller names. A desc tag on a request field becomes that field's description in the tool schema.
type PostInput struct {
	Room         contract.RoomID       `json:"room" desc:"Default: your room. Only root can post plain text to another room's root."`
	Text         string                `json:"text"`
	Mentions     []string              `json:"mentions,omitempty" desc:"parent, children, siblings, or name/ID of your parent or a session under it. Route anything else through your parent; cross-room use root only. Omit to post to the owner."`
	Home         contract.Conversation `json:"home,omitempty" desc:"A session can name only its own DM ({kind: dm}). Omit for the feed if mentioning sessions; otherwise use this turn's owner conversation, the last one, or the feed."`
	ReplyTo      contract.EventID      `json:"replyTo,omitempty"`
	ClientID     string                `json:"clientId,omitempty"`
	VisualUpdate contract.EventID      `json:"visualUpdate,omitempty"`
	VisualReply  contract.EventID      `json:"visualReply,omitempty"`
	Forward      contract.EventID      `json:"forward,omitempty" desc:"Forward a feed post or your room's DM by ID, with optional text. Cross-room forwards omit files."`
	Paths        []string              `json:"paths,omitempty"`
	HTML         string                `json:"html,omitempty"`
	AllowNet     bool                  `json:"allowNet,omitempty"`
	EndTurn      bool                  `json:"endTurn,omitempty" desc:"End the turn on this post; unavailable for cross-room posts."` // read by the box's Pi extension, which alone can end a turn
}

// SkillSaveInput is what a caller names. A box reads a local path itself, so the engine gets the folder.
type SkillSaveInput struct {
	Path     string              `json:"path"`
	Room     contract.RoomID     `json:"room,omitempty"`
	Global   bool                `json:"global,omitempty"`
	RootOnly bool                `json:"rootOnly,omitempty" desc:"Only root sessions' boxes get it."`
	Hosts    []contract.HostName `json:"hosts,omitempty" desc:"Hosts where the skill's commands run; none keeps what the skill has."`
}

// ImageSaveInput is what a caller names. A box reads the folder itself, so the engine gets its archive.
type ImageSaveInput struct {
	Name string          `json:"name"`
	Path string          `json:"path" desc:"The image's folder, with its Containerfile; absolute, or relative to your working directory."`
	Room contract.RoomID `json:"room,omitempty"`
}

type FileRequest struct {
	File string `json:"file" desc:"Absolute, or relative to your working directory."`
}

// The generic type is the concrete request struct named at each row.
func typ[T any]() reflect.Type { return reflect.TypeFor[T]() }

func act(tool, name string, req reflect.Type, help string, cli ...string) Action {
	return Action{Tool: tool, Name: name, Request: req, Help: help, CLI: cli}
}

// opAct routes an action as an operation through op.submit.
func opAct(tool, name string, kind contract.OpKind, req reflect.Type, help, cli string) Action {
	return Action{Tool: tool, Name: name, Request: req, Op: kind, Help: help, CLI: []string{cli}}
}

// MethodRule finds the one authorization row for a method.
func MethodRule(m contract.Method) (MethodEntry, bool) {
	row, ok := methodRows[m]
	return row, ok
}

// Actions returns every registered action across the table.
func Actions() []Action {
	var result []Action
	for _, row := range Methods() {
		result = append(result, row.Actions...)
	}
	return result
}

// BoxActions returns the actions the box agent serves locally.
func BoxActions() []BoxAction {
	return append(visualActions(), computerActions()...)
}

// AgentActions returns the actions an agent may see: actions on methods a
// session may call. Owner-only methods stay hidden until their authorization
// opens; box-local actions are separate.
func AgentActions() []Action {
	var result []Action
	for _, row := range Methods() {
		if row.Access == UserAccess || row.Access == BoxAccess {
			result = append(result, row.Actions...)
		}
	}
	return result
}

// AdvisorInput is what root names when it asks the advisor.
type AdvisorInput struct {
	Prompt string `json:"prompt" desc:"What the advisor needs: the task, what you found so far and what you want judged. It reads your files and session file itself, so point to evidence rather than pasting it."`
}

// ServiceSendInput is what a session names when sending to a feed service; toolCallId is injected by the box.
type ServiceSendInput struct {
	Room    contract.RoomID      `json:"room,omitempty" desc:"Room ID; omitted means your room."`
	Service contract.ServiceName `json:"service" desc:"Feed service name."`
	To      string               `json:"to" desc:"Destination address understood by the service."`
	Text    string               `json:"text" desc:"Message text to send."`
}
