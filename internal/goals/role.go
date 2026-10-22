package goals

import "time"

// KeeperRoomState holds the room's keeper session.
type KeeperRoomState struct {
	Session        string               `json:"session,omitempty"`
	Assignments    int                  `json:"assignments,omitempty"`
	FailedProfiles map[string]time.Time `json:"failedProfiles,omitempty"`
	LastError      string               `json:"lastError,omitempty"`
}
