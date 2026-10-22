// Package filesvc provides workspace browsing and room file HTTP handlers.
package filesvc

import (
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// Service uses room checks and attachment reads supplied by the room service.
type Service struct {
	Rooms           *room.Store
	SessionInRoom   func(roomRef, sessionRef string) (subagent.Info, error)
	AttachmentBytes func(roomRef, id string) ([]byte, error)
}
