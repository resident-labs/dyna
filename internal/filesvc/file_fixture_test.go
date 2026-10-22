package filesvc

import (
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"path/filepath"
	"testing"
)

type fileEnv struct {
	t     *testing.T
	svc   *Service
	rooms *room.Store
}

func newEnv(t *testing.T) *fileEnv {
	t.Helper()
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	svc := &Service{Rooms: rooms, AttachmentBytes: func(roomID, id string) ([]byte, error) {
		path, err := rooms.AttachmentPath(roomID, id)
		if err != nil {
			return nil, err
		}
		return settings.ReadStoredFile(path)
	}}
	return &fileEnv{t: t, svc: svc, rooms: rooms}
}
func (e *fileEnv) room(name string) room.Record {
	e.t.Helper()
	rec, err := e.rooms.Create(room.CreateInput{Name: name})
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}
