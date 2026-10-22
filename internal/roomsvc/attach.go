package roomsvc

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// Attach saves a file with a room for a post that is still being written.
// The bytes are stored before any post names them.
func (s *Service) Attach(roomRef, name string, data []byte) (room.Attachment, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Attachment{}, err
	}
	return s.rooms().PutAttachment(rec.ID, name, data)
}

// AttachmentBytes reads a saved attachment back, for previews.
func (s *Service) AttachmentBytes(roomRef, id string) ([]byte, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	path, err := s.rooms().AttachmentPath(rec.ID, id)
	if err != nil {
		return nil, err
	}
	return settings.ReadStoredFile(path)
}

// AttachBase64 is the desktop and browser upload path. Decode through the
// effective byte limit before allocating the full decoded content. The decoder
// keeps accepting base64 line breaks, as the original upload path did.
func (s *Service) AttachBase64(roomRef, name, encoded string) (room.Attachment, error) {
	limits, err := settings.Content()
	if err != nil {
		return room.Attachment{}, err
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	data, err := io.ReadAll(io.LimitReader(decoder, limits.FileBytes+1))
	if err != nil {
		return room.Attachment{}, err
	}
	if int64(len(data)) > limits.FileBytes {
		return room.Attachment{}, fmt.Errorf("the file is too large (maximum %s)", settings.FormatBytes(int64(limits.FileBytes)))
	}
	return s.Attach(roomRef, name, data)
}

// AttachmentText reads a saved text attachment for the viewer.
func (s *Service) AttachmentText(roomID, id string) (string, error) {
	b, err := s.AttachmentBytes(roomID, id)
	return string(b), err
}
