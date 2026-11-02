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

type PostInput struct {
	Room         contract.RoomID       `json:"room"`
	Text         string                `json:"text"`
	Mentions     []string              `json:"mentions,omitempty"`
	Home         contract.Conversation `json:"home,omitempty"`
	ReplyTo      contract.EventID      `json:"replyTo,omitempty"`
	ClientID     string                `json:"clientId,omitempty"`
	VisualUpdate contract.EventID      `json:"visualUpdate,omitempty"`
	VisualReply  contract.EventID      `json:"visualReply,omitempty"`
	Paths        []string              `json:"paths,omitempty"`
	HTML         string                `json:"html,omitempty"`
	AllowNet     bool                  `json:"allowNet,omitempty"`
	EndTurn      bool                  `json:"endTurn,omitempty"` // read by the box's Pi extension, which alone can end a turn
}

// SkillSaveInput is what a caller names. A box reads a local path itself, so the engine gets the folder.
type SkillSaveInput struct {
	Path   string          `json:"path"`
	Room   contract.RoomID `json:"room,omitempty"`
	Global bool            `json:"global,omitempty"`
}

type FileRequest struct {
	File string `json:"file"`
}
type OpenRequest struct {
	URL string `json:"url"`
}
type SelectorRequest struct {
	Selector string `json:"selector"`
}
type TypeRequest struct {
	Selector string `json:"selector"`
	Text     string `json:"text"`
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
