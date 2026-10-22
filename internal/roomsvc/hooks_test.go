package roomsvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"dyna-agent/internal/hooks"
	"dyna-agent/internal/room"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestHookReposCombinesConfiguredAndActiveRooms(t *testing.T) {
	e := newEnv(t)
	if err := statewrite.MigrateMirrors(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	makeRepo := func() string {
		t.Helper()
		dir := t.TempDir()
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %s, %v", out, err)
		}
		if out, err := exec.Command("git", "-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
			t.Fatalf("git commit: %s, %v", out, err)
		}
		return dir
	}
	configured, active, archived := makeRepo(), makeRepo(), makeRepo()
	if err := hooks.New().Add(context.Background(), configured, hooks.Hook{Name: "check", Event: hooks.AfterLand, Command: "true", Cwd: "main", Timeout: "1s", OnFail: "alert"}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, repo string
		archived   bool
	}{{"configured", configured, false}, {"active", active, false}, {"archived", archived, true}} {
		rec, err := e.svc.rooms().Create(room.CreateInput{Name: tt.name, Repo: tt.repo})
		if err != nil {
			t.Fatal(err)
		}
		if tt.archived {
			if err := e.svc.rooms().SetArchived(rec.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := e.svc.HookRepos(t.Context())
	want := []string{configured, active}
	sort.Strings(want)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("repositories %v, want %v: %v", got, want, err)
	}
}

func TestSpoolWritesWakeHookWorker(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	svc := new(Service)
	version := ""
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	svc.scanHookSpool(&version, true, emit)
	// The scanner observes a spool write, independent of repository identity.
	// Enqueue and durable event contents are covered by the hooks tests.
	spool := filepath.Join(store.DataDir(), "hook-spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "00000000000000000001.json"), []byte(`{"event":"after-land"}`), 0600); err != nil {
		t.Fatal(err)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 1 || events[0].Kind != "hooks" || !events[0].WorkerWake {
		t.Fatalf("events: %#v", events)
	}
	svc.scanHookSpool(&version, false, emit)
	if len(events) != 1 {
		t.Fatal("unchanged spool woke worker")
	}
	version = ""
	svc.scanHookSpool(&version, true, emit)
	if len(events) != 1 {
		t.Fatal("initial scan emitted event")
	}
}
