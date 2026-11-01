package live

import "testing"

func TestTerminalCountsAndRetiredHandle(t *testing.T) {
	l := New(nil)
	handle, ok := l.CreateSession("session")
	if !ok || !handle.OpenTerminal() || !handle.OpenTerminal() {
		t.Fatal("open terminal refused")
	}
	handle.CloseTerminal()
	if !handle.HasTerminal() {
		t.Fatal("one terminal should remain")
	}
	retired := l.RetireSession("session")
	if retired == nil {
		t.Fatal("retire refused")
	}
	retired.Unlock()
	handle.CloseTerminal()
	if handle.HasTerminal() || handle.OpenTerminal() {
		t.Fatal("retired handle remained usable")
	}
}
