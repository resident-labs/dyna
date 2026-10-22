package terminalsvc

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestTermWriteBounds(t *testing.T) {
	s := New(Options{})
	t.Cleanup(s.Shutdown)
	for _, size := range []int{MaxInputBytes, MaxInputBytes + 1, MaxInputBytes + 3} {
		err := s.TermWrite("missing", base64.StdEncoding.EncodeToString(make([]byte, size)))
		if size == MaxInputBytes {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("valid size: %v", err)
			}
		} else if err == nil || err.Error() != "terminal input exceeds 64 KiB" {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	if err := s.TermWrite("missing", "not base64"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid base64: %v", err)
	}
}

func TestTerminalAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	s := New(Options{Shell: "/bin/sh", Directory: func(ref string) (string, string, string, error) {
		if ref != "room:session" {
			t.Fatalf("reference: %q", ref)
		}
		return "room", "session", cwd, nil
	}})
	t.Cleanup(s.Shutdown)
	info, err := s.TermOpen("room:session", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if info.RoomID != "room" || info.SessionID != "session" || info.Cwd != cwd || len(s.List("")) != 1 {
		t.Fatalf("resolved terminal: %+v", info)
	}
	if err := s.TermWrite(info.ID, base64.StdEncoding.EncodeToString([]byte("printf '%s%s\\n' api- bridge\n"))); err != nil {
		t.Fatal(err)
	}
	until(t, s, info.ID, 0, func(ev Event) bool { return strings.Contains(string(ev.Data), "api-bridge") })
	if ev, err := s.TermAttach(info.ID, 0); err != nil || !strings.Contains(string(ev.Data), "api-bridge") {
		t.Fatalf("attach: %+v, %v", ev, err)
	}
	if err := s.Resize(info.ID, 100, 40); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(info.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTermOpenResolverError(t *testing.T) {
	want := errors.New("unknown reference")
	s := New(Options{Directory: func(string) (string, string, string, error) { return "", "", "", want }})
	t.Cleanup(s.Shutdown)
	if _, err := s.TermOpen("missing", 80, 24); !errors.Is(err, want) {
		t.Fatalf("resolver error: %v", err)
	}
	if len(s.List("")) != 0 {
		t.Fatal("opened a shell after resolver failure")
	}
}
