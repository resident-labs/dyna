package terminalsvc

import (
	"encoding/base64"
	"errors"
	"strings"

	"dyna-agent/internal/shellquote"
)

// RequestLines is how many lines of a request terminal the owner may share.
const RequestLines = 20

// OpenRequest starts the owner's login shell for a terminal request. A command
// runs quoted, so no word of it is read as shell syntax, and the shell exits
// with it. With no command the owner gets a plain shell.
func (s *Service) OpenRequest(roomID, sessionID, cwd string, argv []string, cols, rows uint16) (Info, error) {
	if len(argv) == 0 {
		return s.Open(roomID, sessionID, cwd, cols, rows)
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellquote.Quote(arg)
	}
	return s.openShell(roomID, sessionID, cwd, []string{"-c", strings.Join(quoted, " ")}, cols, rows)
}

// AfterExit calls fn once with the terminal's exit code after the process
// ended, however it ended, also when it already has. fn runs in its own
// goroutine, so a closed browser tab cannot lose the call.
func (s *Service) AfterExit(id string, fn func(code int)) error {
	t, err := s.get(id)
	if err != nil {
		return err
	}
	go func() {
		<-t.done
		t.mu.Lock()
		code := t.info.ExitCode
		t.mu.Unlock()
		fn(code)
	}()
	return nil
}

// Info returns one terminal's facts.
func (s *Service) Info(id string) (Info, error) {
	t, err := s.get(id)
	if err != nil {
		return Info{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.info, nil
}

// Tail returns the last n non-empty lines of a terminal's retained output as
// plain text.
func (s *Service) Tail(id string, n int) (string, error) {
	ev, _, err := s.Read(id, 0)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(plainLines(string(ev.Data)), "\n "), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

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
