package view_test

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestHeldCommentsAreOwnerOnly(t *testing.T) {
	e := newEnv(t)
	rec := e.room("held")
	key := "github.com/owner/repo#17"
	held := state.HeldComment{ID: "issue-comments:1", Commenter: "outside", Body: "untrusted-secret-text", Version: "v1"}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
		key: {Key: key, Kind: "github", Host: "github.com", Repo: "owner/repo", Number: 17, RoomID: rec.ID, HeldComments: map[string]state.HeldComment{held.ID: held}, CommentDecisions: map[string]watch.Decision{held.ID: {Action: "hold"}}},
	}})
	for _, role := range []authority.Role{authority.Root, authority.Session, authority.Worker} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role, Room: rec.ID, Session: "agent"})
		if _, err := e.svc.PRs.HeldPRComments(ctx, rec.ID); err == nil {
			t.Fatalf("%s read held text", role)
		}
		if err := e.svc.PRs.AdmitPRComment(ctx, rec.ID, key, held.ID, held.Version); err == nil {
			t.Fatalf("%s admitted held text", role)
		}
		if err := e.svc.PRs.DismissPRComment(ctx, rec.ID, key, held.ID); err == nil {
			t.Fatalf("%s dismissed held text", role)
		}
	}
	rows, err := e.svc.PRs.HeldPRComments(ownerCtx, rec.ID)
	if err != nil || len(rows) != 1 || rows[0].Body != held.Body {
		t.Fatalf("owner rows: %+v, %v", rows, err)
	}
	if err := e.svc.PRs.AdmitPRComment(ownerCtx, rec.ID, key, held.ID, "stale-version"); err == nil {
		t.Fatal("owner admitted unseen replacement text")
	}
	shown, err := e.svc.PRs.Watcher().Show(key)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(shown)
	if strings.Contains(string(data), held.Body) {
		t.Fatal("generic inspection leaked held text")
	}
	if err := e.svc.PRs.DismissPRComment(ownerCtx, rec.ID, key, held.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = e.svc.PRs.HeldPRComments(ownerCtx, rec.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("dismissed rows: %+v, %v", rows, err)
	}
}
