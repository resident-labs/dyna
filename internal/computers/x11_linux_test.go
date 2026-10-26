//go:build linux

package computers

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func listenX11In(t *testing.T, display string) *supervisor {
	t.Helper()
	// Unix socket paths are short, so keep the root directory name short too.
	root, err := os.MkdirTemp("", "x11")
	if err != nil {
		t.Fatal(err)
	}
	old := x11Root
	x11Root = root
	t.Cleanup(func() { x11Root = old; os.RemoveAll(root) })
	return &supervisor{meta: Meta{SessionID: "ses_x", Display: display}}
}

func closeListeners(s *supervisor) {
	for _, l := range s.listeners {
		l.Close()
	}
}

func TestListenX11KeepsFreePreviousDisplay(t *testing.T) {
	s := listenX11In(t, ":250")
	if err := s.listenX11(); err != nil {
		t.Fatal(err)
	}
	defer closeListeners(s)
	if s.meta.Display != ":250" {
		t.Fatalf("display = %s, want the previous :250", s.meta.Display)
	}
}

// A live process, here this test, can hold the previous display's lock without
// owning a computer: the daemon itself does after it loses track of a supervisor.
// The lock then never looks stale, so the computer must take another display.
func TestListenX11FallsBackWhenPreviousDisplayIsHeld(t *testing.T) {
	s := listenX11In(t, ":250")
	lock := filepath.Join(x11Root, ".X250-lock")
	if err := os.WriteFile(lock, []byte(fmt.Sprintf("%10d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.listenX11(); err != nil {
		t.Fatalf("a held previous display left the computer unable to start: %v", err)
	}
	defer closeListeners(s)
	if s.meta.Display == ":250" || s.meta.Display == "" {
		t.Fatalf("display = %q, want a different free display", s.meta.Display)
	}
	if b, _ := os.ReadFile(lock); string(b) != fmt.Sprintf("%10d\n", os.Getpid()) {
		t.Fatal("the held lock was modified")
	}
}
