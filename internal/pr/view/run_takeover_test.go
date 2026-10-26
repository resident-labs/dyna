package view_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// TestRunAgentOpensTrackedPRChangeOfEarlierRunAuthor is the D-252 fix round
// on an already tracked PR, through the public view API: the earlier run
// worker tracked the PR and closed, the room's root session holds the author
// role, and a later worker of the same controller runs
// `dyna git change open --pr 7`.
func TestRunAgentOpensTrackedPRChangeOfEarlierRunAuthor(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	prfixture.FakeGHWithPull(t, watch.Pull{Number: 7, Title: "Add knobs", State: "open"})

	dataDir := filepath.Dir(e.svc.PRs.Watcher().Store.Root)
	root := e.svc.PRs.Notify.Sessions.Store.Root
	controller := "ses_20261021-000001-aaaaaaaa"
	rootSession := "ses_20261021-000002-bbbbbbbb"
	second := "ses_20261021-000003-cccccccc"
	recordtest.Write(t, root, controller, subagent.Meta{ID: controller, RoomID: rec.ID, Room: rec.Name, Profile: "mock"})
	recordtest.Write(t, root, rootSession, subagent.Meta{ID: rootSession, Role: subagent.RoleRoot, RoomID: rec.ID, Room: rec.Name, Profile: "mock"})
	recordtest.Write(t, root, second, subagent.Meta{ID: second, RoomID: rec.ID, Room: rec.Name, Profile: "mock", Run: &subagent.RunOwner{ID: "wf_second"}})
	runstoretest.Write(t, dataDir, runstore.Meta{ID: "wf_second", ParentSession: controller, StartedAt: time.Unix(0, 0)})

	ref := watch.Ref{Host: "github.com", Repo: "acme/widgets", Number: 7}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Records: map[string]*watch.Record{
		ref.Key(): {Key: ref.Key(), ChangeID: ref.Key(), Kind: "github", Host: ref.Host, Repo: ref.Repo, Number: ref.Number, URL: ref.URL(),
			RoomID: rec.ID, Author: rootSession, Controller: state.Controller(controller), Branch: "feat/knobs",
			Head: strings.Repeat("a", 40), State: "open", Watching: true, Created: time.Unix(0, 0)},
	}})

	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Room: rec.ID, Session: second})
	out, err := e.svc.PRs.OpenChange(ctx, rec.ID, second, "7", "", watch.OpenChangeOptions{Notes: watch.ChangeNotes{Intent: "fix round"}})
	if err != nil {
		t.Fatalf("open --pr after the earlier run author closed: %v", err)
	}
	if out.Created || out.Change.Author != second || out.Change.Controller != state.Controller(controller) {
		t.Fatalf("opened PR change: %+v created=%t", out.Change, out.Created)
	}
	if len(out.Change.FormerAuthors) != 1 || out.Change.FormerAuthors[0] != rootSession {
		t.Fatalf("author history: %+v", out.Change.FormerAuthors)
	}
	if out.Change.Notes.Intent != "fix round" {
		t.Fatalf("notes not updated: %+v", out.Change.Notes)
	}
}
