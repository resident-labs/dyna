package live

import "github.com/resident-labs/dyna/contract"

// Computer fields belong to the session lock.
type computerState struct {
	state contract.ComputerState
	room  contract.RoomID
}

// ComputerState returns the current state. The caller holds the session lock.
func (s *Session) ComputerState() contract.ComputerState {
	if s.computer == nil {
		return contract.ComputerStopped
	}
	return s.computer.state
}

// SetComputer changes state after the corresponding event is recorded.
func (s *Session) SetComputer(room contract.RoomID, state contract.ComputerState) {
	if s.computer == nil {
		s.computer = &computerState{state: contract.ComputerStopped, room: room}
	}
	s.computer.state = state
}

// ClearComputer removes the computer and reports whether it was running.
func (s *Session) ClearComputer() (contract.RoomID, bool) {
	c := s.computer
	if c == nil {
		return "", false
	}
	s.computer = nil
	return c.room, c.state == contract.ComputerRunning
}
