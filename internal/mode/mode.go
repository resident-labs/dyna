// Package mode answers "what kind of dyna process is this?". Humans and plain
// agents run dyna with no mode. Every workflow worker runs in workflow mode and
// may never start dyna runs or subagents. A subagent session's worker runs in
// subagent mode and may. The room's internal root session runs in root mode:
// it is stamped with its room but acts as the room owner, like an unstamped
// CLI.
package mode

import (
	"os"
	"strconv"
	"strings"
)

// Environment contract injected into workers.
const (
	EnvMode        = "DYNA_MODE"         // workflow | subagent | root
	EnvWorkerRun   = "DYNA_WORKER_RUN"   // workflow mode: owning run id
	EnvWorkerAgent = "DYNA_WORKER_AGENT" // workflow mode: agent id within that run
	EnvSession     = "DYNA_SESSION"      // subagent mode: session id
	EnvDepth       = "DYNA_DEPTH"        // subagent mode: this session's depth (top level = 1)
	EnvRoomID      = "DYNA_ROOM_ID"      // stamped work: canonical room id
	EnvRoom        = "DYNA_ROOM"         // stamped work: readable room name
	EnvAddress     = "DYNA_ADDRESS"      // stamped session: <room>:<name> address
	EnvRunID       = "DYNA_RUN_ID"       // workflow script: owning run
	EnvRunDetached = "DYNA_RUN_DETACHED" // workflow script: detached runner
	EnvAgentID     = "DYNA_AGENT_ID"     // workflow agent attribution

	Workflow = "workflow"
	Subagent = "subagent"
	Root     = "root"
)

// Current returns the process's mode: Workflow, Subagent, or "".
func Current() string { return strings.TrimSpace(os.Getenv(EnvMode)) }

// InWorkflow reports whether this process is a workflow worker (or a child of one).
func InWorkflow() bool { return Current() == Workflow }

// Session describes the subagent session the process runs inside.
type Session struct {
	ID    string
	Depth int
}

// CurrentSession returns the enclosing subagent session, if any. A bare
// DYNA_SESSION without DYNA_MODE=subagent is the older run-attribution tag, not
// a subagent session. Root mode is not a subagent session either: the
// internal root acts as root.
func CurrentSession() (Session, bool) {
	if Current() != Subagent {
		return Session{}, false
	}
	id := strings.TrimSpace(os.Getenv(EnvSession))
	if id == "" {
		return Session{}, false
	}
	depth, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvDepth)))
	if err != nil || depth < 1 {
		depth = 1
	}
	return Session{ID: id, Depth: depth}, true
}

// Stamp is the room identity Dyna injects into work it launches.
type Stamp struct {
	RoomID  string
	Room    string
	Address string
}

// StampFromEnv returns this process's room stamp, if Dyna injected one.
func StampFromEnv() (Stamp, bool) {
	s := Stamp{
		RoomID:  strings.TrimSpace(os.Getenv(EnvRoomID)),
		Room:    strings.TrimSpace(os.Getenv(EnvRoom)),
		Address: strings.TrimSpace(os.Getenv(EnvAddress)),
	}
	return s, s.RoomID != "" || s.Room != ""
}

// InsideDyna reports whether this process was launched by Dyna as a workflow
// worker or as a subagent session.
func InsideDyna() bool {
	if InWorkflow() {
		return true
	}
	_, ok := CurrentSession()
	return ok
}

// OwnerProcess reports whether this process acts with the room owner's
// authority: a human's shell, an external orchestrator, or the internal root
// (root mode). Every other Dyna stamp is not the owner, including a bare
// run-attribution DYNA_SESSION and a workflow script's room stamp. This is an
// intent guard, not a security boundary.
func OwnerProcess() bool {
	if Current() == Root {
		return true
	}
	_, stamped := StampFromEnv()
	return !stamped && Current() == "" && os.Getenv(EnvSession) == "" && os.Getenv(EnvWorkerRun) == "" && os.Getenv(EnvWorkerAgent) == ""
}
