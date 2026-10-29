package hooks

import (
	"context"
	"testing"
)

// The engine start drops the two librarian hooks that earlier releases added,
// and keeps every other hook, including one with the same name and another command.
func TestRemoveLegacyLibrarianHooksKeepsOtherHooks(t *testing.T) {
	s, repo := setup(t)
	ctx := context.Background()
	wake := "/usr/bin/dyna library wake --repo " + repo
	add(t, s, repo, Hook{Name: "librarian-merged", Event: PRMerged, Command: wake, Cwd: "main", Timeout: "2m", OnFail: "alert"})
	add(t, s, repo, Hook{Name: "librarian-main", Event: MainUpdated, Command: wake, Cwd: "main", Timeout: "2m", OnFail: "alert"})
	add(t, s, repo, Hook{Name: "librarian-batch", Event: Schedule, EveryMinutes: 10, Command: wake, Cwd: "main", Timeout: "2m", OnFail: "alert"})
	add(t, s, repo, Hook{Name: "librarian-check", Event: Schedule, EveryMinutes: 10, Command: "true", Cwd: "main", Timeout: "2m", OnFail: "alert"})
	if err := s.RemoveLegacyLibrarianHooks(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "librarian-main" || list[1].Name != "librarian-check" {
		t.Fatalf("hooks after legacy removal = %+v", list)
	}
	if err := s.RemoveLegacyLibrarianHooks(ctx); err != nil {
		t.Fatalf("second removal: %v", err)
	}
	if again, _ := s.List(ctx, repo); len(again) != 2 {
		t.Fatalf("second removal changed hooks: %+v", again)
	}
}
