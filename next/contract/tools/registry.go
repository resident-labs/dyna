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
	Ancestor
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
func local(tool, action string, handler BoxHandler, req reflect.Type, help, cli string) Entry {
	return Entry{Tool: tool, Action: action, Route: Route{Box: handler}, Request: req, Access: Session, Help: help, CLI: []string{cli}}
}

// The generic type is the concrete request struct named at each registration.
func typ[T any]() reflect.Type { return reflect.TypeFor[T]() }

// Entries returns a fresh copy so callers cannot change the registry.
func Entries() []Entry {
	return []Entry{
		entry("post", "send", contract.MethodPost, typ[contract.PostRequest](), Session, "Send a post", "post"),
		entry("post", "update_visual", contract.MethodPost, typ[contract.PostRequest](), Session, "Update a visual post", "post"),
		entry("session", "start", contract.MethodSessionStart, typ[contract.SessionStartRequest](), Ancestor, "Start a child session", "session start"),
		entry("session", "list", contract.MethodSessionList, typ[contract.SessionListRequest](), Session, "List sessions", "session list"),
		entry("session", "stop", contract.MethodSessionStop, typ[contract.SessionStopRequest](), Ancestor, "Stop a session turn", "session stop"),
		entry("session", "close", contract.MethodSessionClose, typ[contract.SessionCloseRequest](), Ancestor, "Close a session", "session close"),
		entry("session", "restart", contract.MethodSessionRestart, typ[contract.SessionRestartRequest](), Owner, "Replace a failed box", "session restart"),
		entry("session", "set_profile", contract.MethodSessionSetProfile, typ[contract.SessionSetProfileRequest](), Ancestor, "Set a session profile", "session set-profile"),
		entry("session", "move", contract.MethodSessionMove, typ[contract.SessionMoveRequest](), Ancestor, "Move a session", "session move"),
		entry("session", "profiles", contract.MethodProfileList, typ[contract.ProfileListRequest](), Ancestor, "List profiles", "session profiles"),
		entry("room", "log", contract.MethodEventsList, typ[contract.EventsListRequest](), Session, "Read room events", "room log"),
		entry("room", "repos", contract.MethodRoomRepos, typ[contract.RoomReposRequest](), Owner, "List room repositories", "room repo list"),
		entry("room", "pause", contract.MethodRoomPause, typ[contract.RoomPauseRequest](), Ancestor, "Pause or resume a room", "room pause", "room resume"),
		entry("rules", "list", contract.MethodRuleList, typ[contract.RuleListRequest](), Session, "List room rules", "rules"),
		entry("rules", "add", contract.MethodRuleAdd, typ[contract.RuleAddRequest](), Ancestor, "Add a room rule", "rules add"),
		entry("rules", "edit", contract.MethodRuleEdit, typ[contract.RuleEditRequest](), Ancestor, "Edit a room rule", "rules edit"),
		entry("rules", "remove", contract.MethodRuleRemove, typ[contract.RuleRemoveRequest](), Ancestor, "Remove a room rule", "rules rm"),
		entry("op", "list", contract.MethodOpList, typ[contract.OpListRequest](), Session, "List operations", "op list"),
		entry("op", "decide", contract.MethodOpDecide, typ[contract.OpDecideRequest](), Ancestor, "Decide an operation", "op approve", "op deny"),
		entry("op", "withdraw", contract.MethodOpWithdraw, typ[contract.OpWithdrawRequest](), Session, "Withdraw an operation", "op withdraw"),
		entry("hostexec", "run", contract.MethodHostexecRun, typ[contract.HostexecRunRequest](), Session, "Run a host command", "hostexec run"),
		local("visual", "check", VisualCheck, typ[FileRequest](), "Check a visual", "visual check"),
		local("computer", "open", ComputerOpen, typ[OpenRequest](), "Open a page", "computer open"),
		local("computer", "click", ComputerClick, typ[SelectorRequest](), "Click an element", "computer click"),
		local("computer", "type", ComputerType, typ[TypeRequest](), "Type into an element", "computer type"),
		local("computer", "text", ComputerText, typ[SelectorRequest](), "Read page text", "computer text"),
		local("computer", "screenshot", ComputerScreenshot, typ[FileRequest](), "Take a screenshot", "computer screenshot"),
		entry("device", "pair", contract.MethodDevicePair, typ[contract.DevicePairRequest](), Owner, "Pair a device", "device pair"),
		entry("device", "list", contract.MethodDeviceList, typ[contract.DeviceListRequest](), Owner, "List devices", "device list"),
		entry("device", "revoke", contract.MethodDeviceRevoke, typ[contract.DeviceRevokeRequest](), Owner, "Revoke a device", "device revoke"),
		entry("device", "set_scopes", contract.MethodDeviceSetScopes, typ[contract.DeviceSetScopesRequest](), Owner, "Set device scopes", "device set-scopes"),
		entry("room", "add_repo", contract.MethodRoomRepoAdd, typ[contract.RoomRepoAddRequest](), Owner, "Add a repository", "room repo add"),
		entry("hostexec", "revoke", contract.MethodHostexecRevoke, typ[contract.HostexecRevokeRequest](), Owner, "Revoke a host grant", "hostexec revoke"),
		entry("host", "add", contract.MethodHostAdd, typ[contract.HostAddRequest](), Owner, "Add a host", "host add"),
		entry("room", "create", contract.MethodRoomCreate, typ[contract.RoomCreateRequest](), Owner, "Create a room", "room create"),
		entry("room", "limit", contract.MethodRoomSetLimits, typ[contract.RoomSetLimitsRequest](), Owner, "Set room limits", "room limit"),
	}
}

// Validate catches registry mistakes before a schema is advertised.
func Validate(entries []Entry) error {
	seen := make(map[string]bool)
	fields := make(map[string]map[string]string)
	for _, e := range entries {
		key := e.Tool + "/" + e.Action
		if e.Tool == "" || e.Action == "" || e.Help == "" || len(e.CLI) == 0 || e.Access < Session || e.Access > Owner || e.Request == nil || e.Request.Kind() != reflect.Struct || (e.Route.Method == "") == (e.Route.Box == NoBoxHandler) || e.Route.Box > ComputerScreenshot {
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
