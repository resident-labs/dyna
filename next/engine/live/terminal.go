package live

// OpenTerminal records an active terminal unless the session has closed.
func (s *Session) OpenTerminal() bool {
	if !s.Lock() {
		return false
	}
	defer s.Unlock()
	s.terminals++
	return true
}

// CloseTerminal releases a terminal unless the session has closed.
func (s *Session) CloseTerminal() {
	if !s.Lock() {
		return
	}
	defer s.Unlock()
	s.terminals--
}

// HasTerminal reports whether this session has an active terminal.
func (s *Session) HasTerminal() bool {
	if !s.Lock() {
		return false
	}
	defer s.Unlock()
	return s.terminals > 0
}
