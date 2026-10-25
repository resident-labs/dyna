// Package authority owns the role limits shared by services and command help.
package authority

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"dyna-agent/internal/mode"
)

type Role string

const (
	Owner   Role = "owner"
	Root    Role = "root"
	Session Role = "session"
	Worker  Role = "worker"
)

type Relation string

const (
	Author    Relation = "author"
	Parent    Relation = "parent"
	Self      Relation = "self"
	Librarian Relation = "librarian"
	RootRoom  Relation = "root in its own room"
	NotSelf   Relation = "another session"
	Holder    Relation = "current role holder"
)

type Actor struct {
	Role          Role
	Session, Room string
	// Run and Agent name a workflow worker: its run, and its agent in that
	// run, or 0 for the run itself.
	Run   string
	Agent int
}

// WorkerActor is agent of a workflow run, or the run itself when agent is 0.
func WorkerActor(run string, agent int) (Actor, error) {
	run = strings.TrimSpace(run)
	if run == "" {
		return Actor{}, errors.New("a workflow worker needs its run")
	}
	if agent < 0 {
		return Actor{}, fmt.Errorf("workflow worker agent %d is below 0", agent)
	}
	return Actor{Role: Worker, Run: run, Agent: agent}, nil
}

// stampedWorker is the worker that this process's stamp names.
func stampedWorker() (Actor, error) {
	agent := 0
	if stamp := strings.TrimSpace(os.Getenv(mode.EnvWorkerAgent)); stamp != "" {
		n, err := strconv.Atoi(stamp)
		if err != nil || n < 1 {
			return Actor{}, fmt.Errorf("workflow worker agent %q is not a number from 1", stamp)
		}
		agent = n
	}
	return WorkerActor(os.Getenv(mode.EnvWorkerRun), agent)
}

type actorKey struct{}

// WithActor returns ctx carrying the actor a call acts for. The engine sets
// it from the caller on its socket (requirements item 3).
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// EngineRoomActor is the actor of the engine's own action in a room, such as
// closing an issue that a merged PR fixed. The engine decides such an action
// from stored state, not from a caller's request, so it acts as the room's
// root seat. The address that the action shows stays attribution only.
func EngineRoomActor(roomID string) Actor {
	return Actor{Role: Root, Room: roomID}
}

// ActorIn returns the caller's actor when it can act in roomID. The owner
// acts in any room; every other caller is confined to its authenticated room.
func ActorIn(ctx context.Context, roomID string) (Actor, error) {
	actor := Of(ctx)
	if actor.Role != Owner && actor.Room != roomID {
		return Actor{}, errors.New("this caller can make changes only in its own room")
	}
	return actor, nil
}

var inEngine atomic.Bool

// InEngine marks this process as the engine. The engine's own environment is
// the owner's, never a caller's, so from then on Of without an actor returns
// no role, which every Check refuses. restore puts back the previous state,
// for tests of engine-side checks.
func InEngine() (restore func()) {
	was := inEngine.Swap(true)
	return func() { inEngine.Store(was) }
}

// IsEngine reports the process marker established by engine.Run at startup.
// Environment variables, socket paths, and request actors cannot set it.
func IsEngine() bool { return inEngine.Load() }

// ContextActor returns only the authenticated actor carried by ctx.
// It never falls back to process environment stamps.
func ContextActor(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Of returns the actor that ctx carries. Without one, a command's own
// process is described by Current, and the engine refuses.
func Of(ctx context.Context) Actor {
	if a, ok := ContextActor(ctx); ok {
		return a
	}
	if IsEngine() {
		return Actor{}
	}
	return Current()
}

// Current describes the calling process. Services with a sender use that
// sender's identity instead of the engine process's environment, and services
// with a context use Of.
func Current() Actor {
	actor := Actor{Session: strings.TrimSpace(os.Getenv(mode.EnvSession))}
	switch mode.Current() {
	case mode.Root:
		actor.Role = Root
	case mode.Subagent:
		if _, ok := mode.CurrentSession(); ok {
			actor.Role = Session
		}
	case mode.Workflow:
		// A worker without a valid run and agent stamp has no role, so
		// every check refuses it, as the engine refuses such a caller.
		if worker, err := stampedWorker(); err == nil {
			actor.Role, actor.Run, actor.Agent = Worker, worker.Run, worker.Agent
		}
	default:
		// A room stamp selects an owner's room. Session and worker stamps need
		// their corresponding mode before they can identify a caller.
		if mode.Current() == "" && os.Getenv(mode.EnvSession) == "" && os.Getenv(mode.EnvWorkerRun) == "" && os.Getenv(mode.EnvWorkerAgent) == "" {
			actor.Role = Owner
		}
	}
	stamp, _ := mode.StampFromEnv()
	actor.Room = stamp.RoomID
	if actor.Room == "" {
		actor.Room = stamp.Room
	}
	return actor
}

type Rule struct {
	Action    string
	Allowed   []Role
	Relations []Relation
	Instead   string
}

var Rules = []Rule{
	{Action: "git hooks add", Allowed: []Role{Owner, Root}, Instead: "ask root to change stored hook commands"},
	{Action: "git hooks edit", Allowed: []Role{Owner, Root}, Instead: "ask root to change stored hook commands"},
	{Action: "git hooks rm", Allowed: []Role{Owner, Root}, Instead: "ask root to change stored hook commands"},
	{Action: "git sync fetch", Allowed: []Role{Owner, Root, Session, Worker}, Instead: "call through the engine as a registered caller"},
	{Action: "call", Allowed: []Role{Root}, Instead: "use dyna post @parent to ask root to call the owner"},
	{Action: "call list", Allowed: []Role{Root}},
	{Action: "call withdraw", Allowed: []Role{Root}},
	{Action: "git pr track", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to track work"},
	{Action: "git change open", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to open work"},
	{Action: "ask", Allowed: []Role{Root}, Instead: "the human owner only answers questions; use dyna post @parent for questions to root"},
	{Action: "ask withdraw", Allowed: []Role{Root, Session, Worker}, Instead: "the human owner only answers questions; root asks and withdraws with dyna ask"},
	{Action: "room create", Allowed: []Role{Owner, Root}, Instead: "use dyna post @parent for room changes"},
	{Action: "room edit", Allowed: []Role{Owner, Root}, Instead: "use dyna post @parent for room changes"},
	{Action: "room delete", Allowed: []Role{Owner, Root}, Instead: "use dyna post @parent for room changes"},
	{Action: "room watch", Allowed: []Role{Owner}, Instead: "posts arrive as turn input; use dyna post @<mention> for coordination"},
	{Action: "root attach", Allowed: []Role{Owner}, Instead: "run dyna root attach from an external shell"},
	{Action: "root detach", Allowed: []Role{Owner}, Instead: "run dyna root detach from an external shell"},
	{Action: "subagent start", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to start work"},
	{Action: "subagent stop", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control sessions"},
	{Action: "subagent close", Allowed: []Role{Owner, Root}, Relations: []Relation{NotSelf}, Instead: "a session becomes dormant when its turn ends, so it does not close itself"},
	{Action: "subagent reopen", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to start work"},
	{Action: "subagent promote", Allowed: []Role{Owner, Root}, Relations: []Relation{Parent}, Instead: "ask the session parent or root to move it"},
	{Action: "subagent adopt", Allowed: []Role{Owner, Root}, Relations: []Relation{Parent}, Instead: "ask the session parent or root to move it"},
	{Action: "subagent transfer", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control sessions"},
	{Action: "subagent consolidate", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control sessions"},
	{Action: "run", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to start work"},
	{Action: "doctor examine", Allowed: []Role{Owner, Root, Session}, Instead: "post the problem to @root; root or a session runs the doctor"},
	{Action: "doctor report", Allowed: []Role{Owner, Root, Session}, Instead: "post the problem to @root; root or a session reads doctor reports"},
	{Action: "demo", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to start work"},
	{Action: "room pause", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control work"},
	{Action: "room resume", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control work"},
	{Action: "subagent transfer cancel", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control work"},
	{Action: "subagent transfer handoff", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control work"},
	{Action: "subagent consolidate resume", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control work"},
	{Action: "subagent consolidate cancel", Allowed: []Role{Owner, Root, Session}, Instead: "use subagent mode to control work"},
	{Action: "git merge note", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may record notes only in its own room"},
	{Action: "git merge pause", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git merge resume", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git merge cancel", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git merge retry", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git merge bisect", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git merge return", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git merge publish", Allowed: []Role{Owner}, Relations: []Relation{RootRoom}, Instead: "root may control only its own room; use dyna post @parent for merge changes"},
	{Action: "git pr merge-fix", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "SetRoomChangePolicy", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "SetRoomCIBudget", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "git pr review", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "git pr reviewer", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "SetPRSettings", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "git pr noise rm", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "git pr merge-settings", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "git pr publishing", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "settings set", Allowed: []Role{Owner, Root}, Instead: "ask root to change global settings"},
	{Action: "ConfigCheckProfile", Allowed: []Role{Owner, Root}, Instead: "ask root to run a live profile check"},
	{Action: "mcp sync", Allowed: []Role{Owner, Root}, Instead: "ask root to generate private runtime config"},
	{Action: "skill sync", Allowed: []Role{Owner, Root}, Instead: "ask root to generate private runtime config"},
	{Action: "SyncPrivateRuntime", Allowed: []Role{Owner, Root}, Instead: "ask root to generate private runtime config"},
	{Action: "mcp add", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "mcp rm", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "mcp enable", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "mcp disable", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "skill add", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "skill rm", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "skill enable", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "skill disable", Allowed: []Role{Owner, Root}, Instead: "ask root to change the harness catalog"},
	{Action: "profiles add", Allowed: []Role{Owner, Root}, Instead: "ask root to change global profiles"},
	{Action: "profiles set", Allowed: []Role{Owner, Root}, Instead: "ask root to change global profiles"},
	{Action: "profiles remove", Allowed: []Role{Owner, Root}, Instead: "ask root to change global profiles"},
	{Action: "profiles set-default", Allowed: []Role{Owner, Root}, Instead: "ask root to change global profiles"},
	{Action: "profiles enable", Allowed: []Role{Owner, Root}, Instead: "ask root to change global profiles"},
	{Action: "profiles disable", Allowed: []Role{Owner, Root}, Instead: "ask root to change global profiles"},
	// Service profiles are global settings, so only the owner pins them.
	{Action: "SetServiceProfile", Allowed: []Role{Owner}, Instead: "ask the owner to pin a service profile"},
	// The owner's review comments speak for the human owner, so no agent sends them.
	{Action: "SendDiffComments", Allowed: []Role{Owner}, Instead: "ask the owner to send review comments"},
	{Action: "SendWorktreeComments", Allowed: []Role{Owner}, Instead: "ask the owner to send review comments"},
	{Action: "ResolveFinding", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "library enable", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "library disable", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "room clear-dormant", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "subagent dismiss-pending", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "DismissSession", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "ChangeIssue", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "LinkIssueGoal", Allowed: []Role{Owner, Root, Session, Worker}, Instead: "link issues through a goal you can change"},
	{Action: "SetGitHubAccess", Allowed: []Role{Owner, Root}, Instead: "ask the room owner or root to make this change"},
	{Action: "goal expedite", Allowed: []Role{Owner, Root}, Instead: "ask the owner or root to set room priority"},
	{Action: "goal amend", Allowed: []Role{Owner, Root}, Instead: "ask the owner or root to amend the brief"},
	{Action: "post @voice-call", Allowed: []Role{Root}, Instead: "use an allowed mention to reach the room"},
	{Action: "git change reply", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git change close", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git pr untrack", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git pr resume", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git pr require", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git pr hold", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git pr release", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "SetPRGoal", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "SetChangePolicyOverride", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "UpdateChangeNotes", Allowed: []Role{Owner, Root}, Relations: []Relation{Author}, Instead: "ask the author or root to make this change"},
	{Action: "git pr transfer", Allowed: []Role{Owner, Root}, Relations: []Relation{Holder}, Instead: "ask the current role holder or root to transfer it"},
	{Action: "subagent rename", Allowed: []Role{Owner, Root}, Relations: []Relation{Self, Parent}, Instead: "ask the session or its parent to rename it"},
	{Action: "library add", Allowed: []Role{Owner, Root}, Relations: []Relation{Librarian}, Instead: "ask the repository librarian to change its map"},
	{Action: "library update", Allowed: []Role{Owner, Root}, Relations: []Relation{Librarian}, Instead: "ask the repository librarian to change its map"},
	{Action: "library retire", Allowed: []Role{Owner, Root}, Relations: []Relation{Librarian}, Instead: "ask the repository librarian to change its map"},
	{Action: "library smell", Allowed: []Role{Owner, Root}, Relations: []Relation{Librarian}, Instead: "ask the repository librarian to change its map"},
}

func find(action string) (Rule, bool) {
	for _, rule := range Rules {
		if rule.Action == action {
			return rule, true
		}
	}
	return Rule{}, false
}

// Check applies a rule. The service supplies object relationships, if needed.
func Check(action string, actor Actor, has func(Relation) bool) error {
	rule, ok := find(action)
	if !ok {
		return fmt.Errorf("unknown authority action %q", action)
	}
	if actor.Role == "" && inEngine.Load() {
		return fmt.Errorf("%s needs a caller, and this call has none", action)
	}
	for _, role := range rule.Allowed {
		if role == actor.Role {
			return nil
		}
	}
	if has != nil {
		for _, relation := range rule.Relations {
			if relation == RootRoom {
				if actor.Role != Root {
					continue
				}
			} else if actor.Role != Session {
				continue
			}
			if has(relation) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s is %s only; %s", action, who(rule), rule.Instead)
}

func who(rule Rule) string {
	labels := make([]string, 0, len(rule.Allowed)+len(rule.Relations))
	for _, role := range rule.Allowed {
		labels = append(labels, string(role))
	}
	for _, relation := range rule.Relations {
		labels = append(labels, string(relation))
	}
	return strings.Join(labels, " or ")
}

func Who(action string) string {
	rule, ok := find(action)
	if !ok {
		return ""
	}
	return "Who: " + who(rule) + " only."
}

func Guide() string {
	var out strings.Builder
	out.WriteString("## Who can do what\n\n| Action | Who | Instead |\n| --- | --- | --- |\n")
	for _, rule := range Rules {
		fmt.Fprintf(&out, "| `%s` | %s | %s |\n", rule.Action, who(rule), rule.Instead)
	}
	return out.String()
}
