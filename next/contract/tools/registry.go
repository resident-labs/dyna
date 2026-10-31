// Package tools defines the agent and CLI surface as one method table: every
// engine method with its scope rule, who may call it, and the agent actions and
// CLI commands bound to it. The table is the engine gate's authorization
// source; deeper checks, such as room pinning and grants, live in the engine
// services.
package tools

import (
	"fmt"
	"reflect"
	"strings"

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
	for _, row := range Methods() {
		if row.Method == m {
			return row, true
		}
	}
	return MethodEntry{}, false
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

// Method resolves the engine method a registered action routes to.
func Method(tool, name string) contract.Method {
	for _, row := range Methods() {
		for _, a := range row.Actions {
			if a.Tool == tool && a.Name == name {
				return row.Method
			}
		}
	}
	panic("unknown engine action " + tool + "/" + name)
}

// Validate catches table mistakes before a schema is advertised.
func Validate(methods []MethodEntry, box []BoxAction) error {
	rows := make(map[contract.Method]bool, len(methods))
	actions := map[string]bool{}
	fields := map[string]map[string]string{}
	for _, m := range methods {
		if m.Method == "" || m.Access < OwnerAccess || m.Access > OwnerLocalAccess {
			return fmt.Errorf("invalid method row %q", m.Method)
		}
		if rows[m.Method] {
			return fmt.Errorf("duplicate method row %q", m.Method)
		}
		rows[m.Method] = true
		for _, a := range m.Actions {
			key := a.Tool + "/" + a.Name
			if a.Tool == "" || a.Name == "" || a.Help == "" || len(a.CLI) == 0 || a.Request == nil || a.Request.Kind() != reflect.Struct {
				return fmt.Errorf("invalid action %s", key)
			}
			if a.Op != "" && (m.Method != contract.MethodOpSubmit || (a.Op != (contract.PrOpenArgs{}).OpKind() && a.Op != (contract.HostexecGrantArgs{}).OpKind())) {
				return fmt.Errorf("unknown operation route %s", key)
			}
			for _, binding := range a.CLI {
				if strings.TrimSpace(binding) == "" {
					return fmt.Errorf("empty CLI binding %s", key)
				}
			}
			if actions[key] {
				return fmt.Errorf("duplicate action %s", key)
			}
			actions[key] = true
			if err := checkFields(fields, a.Tool, a.Request); err != nil {
				return err
			}
		}
	}
	for _, b := range box {
		key := b.Tool + "/" + b.Name
		if b.Tool == "" || b.Name == "" || b.Help == "" || len(b.CLI) == 0 || b.Request == nil || b.Request.Kind() != reflect.Struct || b.Handler == NoBoxHandler || b.Handler > ComputerScreenshot {
			return fmt.Errorf("invalid box action %s", key)
		}
		if actions[key] {
			return fmt.Errorf("duplicate action %s", key)
		}
		actions[key] = true
		if err := checkFields(fields, b.Tool, b.Request); err != nil {
			return err
		}
	}
	return nil
}

func checkFields(fields map[string]map[string]string, tool string, req reflect.Type) error {
	if fields[tool] == nil {
		fields[tool] = map[string]string{}
	}
	for i := 0; i < req.NumField(); i++ {
		f := req.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		kind := schemaShape(f.Type)
		if old, ok := fields[tool][name]; ok && old != kind {
			return fmt.Errorf("conflicting field %s.%s", tool, name)
		}
		fields[tool][name] = kind
	}
	return nil
}

// schemaShape compares the JSON shapes, not Go aliases such as RoomID.
func schemaShape(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return t.Kind().String() + "[" + schemaShape(t.Elem()) + "]"
	case reflect.Struct:
		var parts []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				parts = append(parts, name+":"+schemaShape(f.Type))
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	case reflect.Map:
		return "map[" + schemaShape(t.Key()) + "]" + schemaShape(t.Elem())
	default:
		return t.Kind().String()
	}
}
