package tools

import "github.com/resident-labs/dyna/contract"

func sessionEntries() []Entry {
	return []Entry{
		entry("session", "start", contract.MethodSessionStart, typ[contract.SessionStartRequest](), Tree, "Start a child session", "session start"),
		entry("session", "list", contract.MethodSessionList, typ[contract.SessionListRequest](), Session, "List sessions", "session list"),
		entry("session", "stop", contract.MethodSessionStop, typ[contract.SessionStopRequest](), Tree, "Stop a session turn", "session stop"),
		entry("session", "close", contract.MethodSessionClose, typ[contract.SessionCloseRequest](), Tree, "Close a session", "session close"),
		entry("session", "restart", contract.MethodSessionRestart, typ[contract.SessionRestartRequest](), Tree, "Replace a failed box", "session restart"),
		entry("session", "set_profile", contract.MethodSessionSetProfile, typ[contract.SessionSetProfileRequest](), Tree, "Set a session profile", "session set-profile"),
		entry("session", "move", contract.MethodSessionMove, typ[contract.SessionMoveRequest](), Tree, "Move a session", "session move"),
		entry("session", "profiles", contract.MethodProfileList, typ[contract.ProfileListRequest](), Tree, "List profiles", "session profiles"),
	}
}
