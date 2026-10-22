// Package view provides PR and GitHub data for Dyna clients.
package view

import (
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/subagent"
)

// Service reads PR state with room checks supplied by its caller.
type Service struct {
	// Now supplies the same clock to review facts and committed view expiry.
	Now           func() time.Time
	projection    projectionCache
	Notify        *notify.Service
	SessionInRoom func(roomRef, sessionRef string) (subagent.Info, error)
	WorkspaceRoot func(roomRef, sessionRef string) (string, error)
}
