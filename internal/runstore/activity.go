package runstore

import "log"

// ActiveRoomIDs returns rooms with a running workflow whose process still exists.
func ActiveRoomIDs() map[string]bool {
	live := map[string]bool{}
	runs, err := Running()
	if err != nil {
		log.Printf("active rooms: runs: %v", err)
	}
	for _, m := range runs {
		if m.RoomID != "" && Alive(m) {
			live[m.RoomID] = true
		}
	}
	return live
}
