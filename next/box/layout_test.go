package box_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestLayoutKeepsARoomInOneFolder(t *testing.T) {
	l := box.Layout{Data: "/data", HostData: "/host"}
	for got, want := range map[string]string{
		l.Instructions(""):      "/data/AGENTS.md",
		l.Instructions("r"):     "/data/rooms/r/AGENTS.md",
		l.File("r", "abc"):      "/data/rooms/r/files/abc",
		l.Transcripts("r", "s"): "/data/rooms/r/transcripts/s",
		l.HostInstructions(""):  "/host/instructions/global/AGENTS.md",
		l.HostInstructions("r"): "/host/rooms/r/instructions/AGENTS.md",
		l.HostShared("r"):       "/host/rooms/r/shared",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

func TestLayoutRefusesARoomThatNamesMoreThanOneFolder(t *testing.T) {
	l := box.Layout{Data: "/data", HostData: "/host"}
	for _, room := range []contract.RoomID{"", ".", "..", "a/b", `a\b`} {
		if dir, err := l.Room(room); err == nil {
			t.Errorf("room %q: %s", room, dir)
		}
		if dir, err := l.HostRoom(room); err == nil {
			t.Errorf("host room %q: %s", room, dir)
		}
	}
	if dir, err := l.Room("room_1"); err != nil || dir != "/data/rooms/room_1" {
		t.Fatalf("%s: %v", dir, err)
	}
	if dir, err := l.HostRoom("room_1"); err != nil || dir != "/host/rooms/room_1" {
		t.Fatalf("%s: %v", dir, err)
	}
}

func TestFindFileLooksInEveryRoom(t *testing.T) {
	l := box.Layout{Data: t.TempDir()}
	if _, found := l.FindFile("abc"); found {
		t.Fatal("found a file with no rooms")
	}
	path := l.File("r", "abc")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Files("empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, found := l.FindFile("abc"); !found || got != path {
		t.Fatalf("%s %v", got, found)
	}
}
