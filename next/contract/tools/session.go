package tools

import "github.com/resident-labs/dyna/contract"

func sessionEntries() []Entry {
	return []Entry{
		entry("session", "start", contract.MethodSessionStart, typ[contract.SessionStartRequest](), Tree, "Start a child session. Give room, name and profile. Omit parent to use the caller; repo, goal, brief, size and sameHost are optional.", "session start"),
		entry("session", "list", contract.MethodSessionList, typ[contract.SessionListRequest](), Session, "List sessions in room.", "session list"),
		entry("session", "stop", contract.MethodSessionStop, typ[contract.SessionStopRequest](), Tree, "Stop the running turn of session.", "session stop"),
		entry("session", "close", contract.MethodSessionClose, typ[contract.SessionCloseRequest](), Tree, "Close session. Set discard to skip its archive.", "session close"),
		entry("session", "restart", contract.MethodSessionRestart, typ[contract.SessionRestartRequest](), Tree, "Replace the failed box for session; unpushed work is lost.", "session restart"),
		entry("session", "set_profile", contract.MethodSessionSetProfile, typ[contract.SessionSetProfileRequest](), Tree, "Set profile for session.", "session set-profile"),
		entry("session", "move", contract.MethodSessionMove, typ[contract.SessionMoveRequest](), Tree, "Move session under newParent.", "session move"),
	}
}
