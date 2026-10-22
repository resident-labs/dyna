package view_test

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestOwnerTracksAndOpensPRWithoutSession(t *testing.T) {
	for _, method := range []string{"track", "open"} {
		t.Run(method, func(t *testing.T) {
			e, rec, root := workspaceEnv(t)
			pull := watch.Pull{Number: 7, Title: "Owner work", State: "open"}
			pull.Head.SHA = strings.Repeat("a", 40)
			prfixture.FakeGHWithPull(t, pull)
			restore := authority.InEngine()
			defer restore()
			call := func(ctx context.Context) (watch.Record, error) {
				if method == "track" {
					out, err := e.svc.PRs.TrackPRRef(ctx, rec.ID, "", "7", "")
					return out.PR, err
				}
				out, err := e.svc.PRs.OpenChange(ctx, rec.ID, "", "7", "", watch.OpenChangeOptions{Notes: watch.ChangeNotes{Intent: "Owner work"}})
				return out.Change, err
			}
			for _, role := range []authority.Role{authority.Session, authority.Root, authority.Worker, authority.Steward, ""} {
				ctx := authority.WithActor(t.Context(), authority.Actor{Role: role, Room: rec.ID, Session: "ses_20261021-000000-0000abcd"})
				if _, err := call(ctx); err == nil {
					t.Fatalf("%s gained an owner author through an empty session reference", role)
				}
			}
			if _, err := call(t.Context()); err == nil {
				t.Fatal("an unauthenticated engine call gained an owner author")
			}
			before, err := e.svc.PRs.Watcher().Store.Read()
			if err != nil || len(before.Records) != 0 {
				t.Fatalf("refused caller stored PR state: %v", err)
			}
			got, err := call(ownerCtx)
			if err != nil {
				t.Fatalf("authenticated owner with no session: %v", err)
			}
			if got.Author != "" || got.Parent != "" || got.RoomID != rec.ID || got.Dir != root || got.LookupDir != root || !got.Watching {
				t.Fatalf("owner author lost its room or workspace: %+v", got)
			}
		})
	}
}
