package watch

import "dyna-agent/internal/room"

// wakesRoot reports whether the writer classifies msg as a post that asks the
// root seat to act: room.RootContext stores such a post as the seat's waking
// delivery row, and every reader of the seat reads that row.
func wakesRoot(msg room.Message, roomName string) bool {
	decision, _ := room.RootContext(msg, roomName)
	return decision == room.RootWake
}
