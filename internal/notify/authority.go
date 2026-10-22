package notify

import (
	"dyna-agent/internal/authority"
	"dyna-agent/internal/subagent"
)

// senderActor uses the sender identity, not the engine process identity.
func senderActor(snd Sender) authority.Actor {
	actor := authority.Actor{Role: authority.Worker, Session: snd.Session.ID, Room: snd.Room, Run: snd.RunID, Agent: snd.Agent}
	switch snd.Kind {
	case SenderOwner:
		actor.Role = authority.Root
		if snd.Human {
			actor.Role = authority.Owner
		}
	case SenderSession:
		actor.Role = authority.Session
		if snd.Session.Role == subagent.RoleRoot {
			actor.Role = authority.Root
		}
	}
	return actor
}
