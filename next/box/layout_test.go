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
	room, err := l.Room("r")
	if err != nil {
		t.Fatal(err)
	}
	hostRoom, err := l.HostRoom("r")
	if err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{
		l.Instructions("r"):     room + "/AGENTS.md",
		l.Compose("r"):          room + "/compose",
		l.File("r", "abc"):      room + "/files/abc",
		l.Transcripts("r", "s"): room + "/transcripts/s",
		l.HostInstructions("r"): hostRoom + "/instructions/AGENTS.md",
		l.HostShared("r"):       hostRoom + "/shared",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	if room != "/data/rooms/r" || hostRoom != "/host/rooms/r" {
		t.Errorf("rooms at %s and %s", room, hostRoom)
	}
}

func TestLayoutNamesWhatNoRoomOwns(t *testing.T) {
	l := box.Layout{Data: "/data", HostData: "/host"}
	mirror, err := l.Git("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := l.HostGit("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	for got, want := range map[string]string{
		l.Instructions(""):      "/data/AGENTS.md",
		l.Compose(""):           "/data/compose",
		l.Store():               "/data/store.db",
		l.StoreLock():           "/data/store.lock",
		l.V1Store():             "/data/dyna.db",
		l.Backups():             "/data/backups",
		l.Archive():             "/data/archive",
		l.Connect():             "/data/connect",
		l.Proxy():               "/data/cliproxy",
		l.ProxyAuth():           "/data/cliproxy/auth-v2",
		l.Rooms():               "/data/rooms",
		mirror:                  "/data/git/owner/repo.git",
		l.HostInstructions(""):  "/host/instructions/global/AGENTS.md",
		l.HostGitRoot():         "/host/git",
		cache:                   "/host/git/owner/repo.git",
		l.HostBin():             "/host/bin",
		l.HostCache():           "/host/cache",
		l.HostSocketFolder("b"): "/host/sock/b",
		l.HostSocket("b"):       "/host/sock/b/engine.sock",
		l.HostProbeSocket("t"):  "/host/sock/probe-t.sock",
		l.HostRooms():           "/host/rooms",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

func TestLayoutRefusesARepositoryThatLeavesTheGitFolder(t *testing.T) {
	l := box.Layout{Data: "/data", HostData: "/host"}
	for _, repo := range []contract.RepoName{"", "repo", "a/b/c", "../repo", "owner/..", "owner/.", "/repo", "owner/", "owner/re po", "owner/re:po"} {
		if dir, err := l.Git(repo); err == nil {
			t.Errorf("repo %q: %s", repo, dir)
		}
		if dir, err := l.HostGit(repo); err == nil {
			t.Errorf("host repo %q: %s", repo, dir)
		}
	}
	if dir, err := (box.Layout{}).Git("owner/repo"); err == nil {
		t.Errorf("an unset root named %s", dir)
	}
	if dir, err := (box.Layout{HostData: "host"}).HostGit("owner/repo"); err == nil {
		t.Errorf("a relative root named %s", dir)
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
	if dir, err := (box.Layout{}).Room("room_1"); err == nil {
		t.Errorf("an unset root named %s", dir)
	}
	if dir, err := (box.Layout{HostData: "host"}).HostRoom("room_1"); err == nil {
		t.Errorf("a relative root named %s", dir)
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
