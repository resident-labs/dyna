package terminalsvc

import (
	"encoding/base64"
	"errors"
)

// TermOpen starts a shell in the directory selected by the process wiring.
func (s *Service) TermOpen(ref string, cols, rows uint16) (Info, error) {
	if s.options.Directory == nil {
		return Info{}, errors.New("terminal directory resolver is not configured")
	}
	roomID, sessionID, cwd, err := s.options.Directory(ref)
	if err != nil {
		return Info{}, err
	}
	return s.Open(roomID, sessionID, cwd, cols, rows)
}

// TermAttach returns scrollback without the internal output wake channel.
func (s *Service) TermAttach(id string, after uint64) (Event, error) {
	ev, _, err := s.Read(id, after)
	return ev, err
}

// TermWrite decodes bounded base64 input before writing it to the terminal.
func (s *Service) TermWrite(id, data string) error {
	if len(data) > (MaxInputBytes+2)/3*4 {
		return errors.New("terminal input exceeds 64 KiB")
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	return s.Write(id, raw)
}
