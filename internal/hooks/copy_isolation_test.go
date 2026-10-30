package hooks

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/processguard"
)

func TestCopiedHooksCannotRun(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	old := Hook{Name: "old", Event: Schedule, Command: "true", Cwd: t.TempDir(), Timeout: "1s", OnFail: "alert", EveryMinutes: 1}
	if err := s.AddGlobal(ctx, old); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(s.dataDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE dyna_copy_marker (id INTEGER PRIMARY KEY, copied_at TEXT NOT NULL); INSERT INTO dyna_copy_marker VALUES (1, ?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	called := false
	s.Exec = func(context.Context, processguard.Process) error { called = true; return nil }
	if _, err := s.RunGlobal(ctx, Event{}, "old"); err == nil || !strings.Contains(err.Error(), "copied hooks") || called {
		t.Fatalf("run copied hook: called=%v err=%v", called, err)
	}
	newHook := old
	newHook.Name = "new"
	if err := s.AddGlobal(ctx, newHook); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunGlobal(ctx, Event{}, "new"); err != nil || !called {
		t.Fatalf("run new hook: called=%v err=%v", called, err)
	}
}
