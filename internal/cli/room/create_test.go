package room

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/roomsvc"
	dynastore "dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

// serveRoomCreate serves the engine's CreateRoomRecord call on the same room
// service the engine runs. This stand-in keeps the CLI's creator; the
// engine's creator rules are tested in internal/engine.
func serveRoomCreate(t *testing.T) {
	t.Helper()
	svc := roomsvc.New(nil)
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "CreateRoomRecord", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.CreateRoomRecord},
		{Name: "SetRoomRootProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: svc.SetRoomRootProfile},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, dynastore.DataDir(), calls)
}

// With no engine, no room is created and dyna room create exits non-zero
// with the cause (requirement 26).
func TestRoomCreateWithoutEngineFailsWithTheCause(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saved := engineClient
	t.Cleanup(func() { engineClient = saved })
	engineClient = func() *engineclient.Client {
		return engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	t.Chdir(t.TempDir())
	_, err := execCmd(t, RoomCmd(), "create", "notes")
	if err == nil || !strings.Contains(err.Error(), "the room was not created") || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("create without engine: %v", err)
	}
	if recs, err := store().List(true); err != nil || len(recs) != 0 {
		t.Fatalf("rooms = %+v, %v; want none", recs, err)
	}
}

func TestRoomCreateNamesRoomsByRepo(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveRoomCreate(t)
	repo := filepath.Join(t.TempDir(), "Web_App")
	gitInit(t, repo)
	sub := filepath.Join(repo, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := execCmd(t, RoomCmd(), "create", "--repo", repo)
	if err != nil || strings.TrimSpace(first) != "web-app" {
		t.Fatalf("create --repo = %q, %v", first, err)
	}
	t.Chdir(sub)
	again, err := execCmd(t, RoomCmd(), "create")
	if err != nil || again != first {
		t.Fatalf("create in repo cwd = %q, %v; want %q", again, err, first)
	}
	named, err := execCmd(t, RoomCmd(), "create", "notes")
	if err != nil || named != first {
		t.Fatalf("create notes = %q, %v", named, err)
	}

	explicit, err := execCmd(t, RoomCmd(), "create", "other", "--repo", repo)
	if err != nil || explicit != first {
		t.Fatalf("named --repo = %q, %v; want %q", explicit, err, first)
	}
	t.Chdir(t.TempDir())
	notes, err := execCmd(t, RoomCmd(), "create", "notes")
	if err != nil || strings.TrimSpace(notes) != "notes" {
		t.Fatalf("non-repo notes = %q, %v", notes, err)
	}

	_, err = execCmd(t, RoomCmd(), "create")
	if err == nil || !strings.Contains(err.Error(), "not inside a git repo") || !strings.Contains(err.Error(), "dyna room create <name>") {
		t.Fatalf("create outside git err = %v", err)
	}
	if _, err := execCmd(t, RoomCmd(), "create", "--repo", t.TempDir()); err == nil {
		t.Fatal("create --repo on a non-git dir succeeded")
	}
}

func TestRoomCreateReusesReservedRepoName(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveRoomCreate(t)
	repo := filepath.Join(t.TempDir(), "global")
	gitInit(t, repo)
	first, err := execCmd(t, RoomCmd(), "create", "custom", "--repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, args := range [][]string{{"create"}, {"create", "--repo", repo}} {
		got, err := execCmd(t, RoomCmd(), args...)
		if err != nil || got != first {
			t.Fatalf("%v = %q, %v; want %q", args, got, err, first)
		}
	}
}

func TestRoomRootProfileCreateEditAndClear(t *testing.T) {
	clearStamp(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	serveRoomCreate(t)
	t.Chdir(t.TempDir())

	if _, err := execCmd(t, RoomCmd(), "create", "notes", "--root-profile", " chosen "); err != nil {
		t.Fatal(err)
	}
	assertRootProfile := func(want string) {
		t.Helper()
		rec, err := store().Resolve("notes")
		if err != nil || rec.RootProfile != want {
			t.Fatalf("room root profile = %q, %v; want %q", rec.RootProfile, err, want)
		}
	}
	assertRootProfile("chosen")
	if _, err := execCmd(t, RoomCmd(), "edit", "notes", "--root-profile", "other"); err != nil {
		t.Fatal(err)
	}
	assertRootProfile("other")
	// Reusing a room must keep its saved choice.
	if _, err := execCmd(t, RoomCmd(), "create", "notes"); err != nil {
		t.Fatal(err)
	}
	assertRootProfile("other")
	if _, err := execCmd(t, RoomCmd(), "edit", "notes", "--root-profile", ""); err != nil {
		t.Fatal(err)
	}
	assertRootProfile("")
}
