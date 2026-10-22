package roomsvc

import (
	"dyna-agent/internal/store"
	"os"
	"path/filepath"
)

func (s *Service) scanSync(previous *string, first bool, emit func(Event)) {
	version := ""
	if info, err := os.Stat(filepath.Join(store.DataDir(), "sync.json")); err == nil {
		version = info.ModTime().String()
	}
	if version != *previous {
		*previous = version
		if !first {
			emit(Event{Kind: "sync", WorkerWake: true})
		}
	}
}
