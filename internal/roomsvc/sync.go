package roomsvc

import (
	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func (s *Service) scanSync(previous *string, first bool, emit func(Event)) {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return
	}
	var version string
	if err := db.QueryRow("SELECT revision FROM lane_sync WHERE id=1").Scan(&version); err != nil {
		return
	}
	if version != *previous {
		*previous = version
		if !first {
			emit(Event{Kind: "sync", WorkerWake: true})
		}
	}
}
