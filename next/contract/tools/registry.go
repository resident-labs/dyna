// Package tools defines the agent and CLI surface. The engine remains the authority.
package tools

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type Access uint8

const (
	Session Access = iota + 1
	Tree
	Grant
	Owner
)

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

type Route struct {
	Method contract.Method
	Box    BoxHandler
	Op     contract.OpKind
}
type Entry struct {
	Tool, Action string
	Route        Route
	Request      reflect.Type
	Access       Access
	Help         string
	CLI          []string
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

func entry(tool, action string, method contract.Method, req reflect.Type, access Access, help string, cli ...string) Entry {
	return Entry{Tool: tool, Action: action, Route: Route{Method: method}, Request: req, Access: access, Help: help, CLI: cli}
}
func operation(tool, action string, kind contract.OpKind, req reflect.Type, access Access, help, cli string) Entry {
	return Entry{Tool: tool, Action: action, Route: Route{Op: kind}, Request: req, Access: access, Help: help, CLI: []string{cli}}
}
func local(tool, action string, handler BoxHandler, req reflect.Type, help, cli string) Entry {
	return Entry{Tool: tool, Action: action, Route: Route{Box: handler}, Request: req, Access: Session, Help: help, CLI: []string{cli}}
}

// The generic type is the concrete request struct named at each registration.
func typ[T any]() reflect.Type { return reflect.TypeFor[T]() }

// Entries returns a fresh copy so callers cannot change the registry.
func Entries() []Entry {
	var entries []Entry
	entries = append(entries, postEntries()...)
	entries = append(entries, sessionEntries()...)
	entries = append(entries, roomEntries()...)
	entries = append(entries, rulesEntries()...)
	entries = append(entries, opEntries()...)
	entries = append(entries, hostexecEntries()...)
	entries = append(entries, prEntries()...)
	entries = append(entries, contextEntries()...)
	entries = append(entries, visualEntries()...)
	entries = append(entries, computerEntries()...)
	entries = append(entries, deviceEntries()...)
	entries = append(entries, hostEntries()...)
	return entries
}

// Validate catches registry mistakes before a schema is advertised.
func Validate(entries []Entry) error {
	seen := make(map[string]bool)
	fields := make(map[string]map[string]string)
	for _, e := range entries {
		key := e.Tool + "/" + e.Action
		if e.Tool == "" || e.Action == "" || e.Help == "" || len(e.CLI) == 0 || e.Access < Session || e.Access > Owner || e.Request == nil || e.Request.Kind() != reflect.Struct || routeCount(e.Route) != 1 || e.Route.Box > ComputerScreenshot || (e.Route.Op != "" && e.Route.Op != (contract.PrOpenArgs{}).OpKind() && e.Route.Op != (contract.HostexecGrantArgs{}).OpKind()) {
			return fmt.Errorf("invalid entry %s", key)
		}
		for _, binding := range e.CLI {
			if strings.TrimSpace(binding) == "" {
				return fmt.Errorf("empty CLI binding %s", key)
			}
		}
		if seen[key] {
			return fmt.Errorf("duplicate entry %s", key)
		}
		seen[key] = true
		if fields[e.Tool] == nil {
			fields[e.Tool] = make(map[string]string)
		}
		for i := 0; i < e.Request.NumField(); i++ {
			f := e.Request.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			kind := schemaShape(f.Type)
			if old, ok := fields[e.Tool][name]; ok && old != kind {
				return fmt.Errorf("conflicting field %s.%s", e.Tool, name)
			}
			fields[e.Tool][name] = kind
		}
	}
	return nil
}

func AgentEntries() []Entry {
	var result []Entry
	for _, e := range Entries() {
		if e.Access != Owner {
			result = append(result, e)
		}
	}
	return result
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

// Method resolves a registered engine action for a CLI caller.
func Method(tool, action string) contract.Method {
	for _, e := range Entries() {
		if e.Tool == tool && e.Action == action && e.Route.Method != "" {
			return e.Route.Method
		}
	}
	panic("unknown engine action " + tool + "/" + action)
}

func routeCount(r Route) int {
	n := 0
	if r.Method != "" {
		n++
	}
	if r.Box != NoBoxHandler {
		n++
	}
	if r.Op != "" {
		n++
	}
	return n
}
