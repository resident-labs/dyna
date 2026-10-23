package view_test

import (
	"context"
	"reflect"
	"testing"

	"dyna-agent/internal/authority"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestRequireReviewersAuthorLeadRootAndOwnerPermissions(t *testing.T) {
	e := newEnv(t)
	rm, other := e.room("require-alpha"), e.room("require-beta")
	const parent = "ses_20261020-000001-aaaabbbb"
	const author = "ses_20261020-000002-aaaabbbb"
	const unrelated = "ses_20261020-000003-aaaabbbb"
	const reviewer = "ses_20261020-000004-aaaabbbb"
	root := e.svc.PRs.Notify.Sessions.Store.Root
	for id, name := range map[string]string{parent: "lead", author: "author", unrelated: "unrelated", reviewer: "architecture"} {
		m := subagent.Meta{ID: id, Name: name, RoomID: rm.ID, Room: rm.Name}
		if id == author {
			m.ParentSession = parent
		}
		recordtest.Write(t, root, id, m)
	}
	st, err := e.svc.PRs.Watcher().Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: rm.ID, State: "open", Watching: true, Head: "head", Author: author}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	as := func(role authority.Role, id, room string) context.Context {
		return authority.WithActor(t.Context(), authority.Actor{Role: role, Session: id, Room: room})
	}
	for _, ctx := range []context.Context{as(authority.Session, unrelated, rm.ID), as(authority.Root, parent, other.ID), as(authority.Worker, "", rm.ID)} {
		before, err := e.svc.PRs.Watcher().Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.svc.PRs.RequirePRReviewers(ctx, rm.ID, "change", []string{"architecture"}); err == nil {
			t.Fatal("unrelated caller set required reviews")
		}
		after, err := e.svc.PRs.Watcher().Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("refused caller changed PR state")
		}
	}
	for _, ctx := range []context.Context{as(authority.Session, author, rm.ID), as(authority.Session, parent, rm.ID), as(authority.Root, parent, rm.ID), ownerCtx} {
		if err := e.svc.PRs.RequirePRReviewers(ctx, rm.ID, "change", []string{"architecture", reviewer}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.svc.PRs.Watcher().Store.Get("change")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.RequiredReviewers, []string{reviewer}) {
		t.Fatalf("name and ID were not canonicalized: %v", got.RequiredReviewers)
	}
}
