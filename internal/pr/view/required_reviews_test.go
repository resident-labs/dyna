package view_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/facts"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// Releasing a required reviewer must work even when its session is closed:
// that is the manual path that keeps a stalled PR moving (D-64).
func TestReleasePRReviewersRemovesRequirementIncludingClosedSession(t *testing.T) {
	e := newEnv(t)
	rm := e.room("release-alpha")
	const author = "ses_20261020-000002-aaaabbbb"
	const unrelated = "ses_20261020-000003-aaaabbbb"
	const reviewer = "ses_20261020-000004-aaaabbbb"
	const openReviewer = "ses_20261020-000005-aaaabbbb"
	root := e.svc.PRs.Notify.Sessions.Store.Root
	for id, name := range map[string]string{author: "author", unrelated: "unrelated", reviewer: "architecture", openReviewer: "safety"} {
		recordtest.Write(t, root, id, subagent.Meta{ID: id, Name: name, RoomID: rm.ID, Room: rm.Name})
	}
	w := e.svc.PRs.Watcher()
	st, err := w.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: rm.ID, State: "open", Watching: true, Head: "head", Author: author, RequiredReviewers: []string{reviewer, openReviewer}}
	prfixture.SaveState(t, w.Store.Root, st)
	as := func(role authority.Role, id string) context.Context {
		return authority.WithActor(t.Context(), authority.Actor{Role: role, Session: id, Room: rm.ID})
	}
	// The requirements block the change while both reviewers still owe verdicts.
	got, err := w.Store.Get("change")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.MissingRequiredReviewers(got)) != 2 {
		t.Fatal("setup has no missing required reviews")
	}
	if err := e.svc.PRs.ReleasePRReviewers(as(authority.Session, unrelated), rm.ID, "change", []string{"architecture"}); err == nil {
		t.Fatal("unrelated caller released a required reviewer")
	}
	// An open reviewer is released by its room name.
	if err := e.svc.PRs.ReleasePRReviewers(as(authority.Session, author), rm.ID, "change", []string{"safety"}); err != nil {
		t.Fatalf("author could not release the open required reviewer: %v", err)
	}
	// The remaining reviewer's session closed before its verdict; the release
	// by session ID must not depend on the session being open.
	recordtest.Write(t, root, reviewer, subagent.Meta{ID: reviewer, Name: "architecture", RoomID: rm.ID, Room: rm.Name, Closed: time.Unix(1, 0)})
	if err := e.svc.PRs.ReleasePRReviewers(ownerCtx, rm.ID, "change", []string{reviewer}); err != nil {
		t.Fatalf("owner could not release the closed required reviewer: %v", err)
	}
	got, err = w.Store.Get("change")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RequiredReviewers) != 0 || len(facts.MissingRequiredReviewers(got)) != 0 {
		t.Fatalf("release left a requirement in place: %+v", got.RequiredReviewers)
	}
	// Releasing a reviewer that is not required is a no-op, not an error.
	before, err := w.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PRs.ReleasePRReviewers(ownerCtx, rm.ID, "change", []string{reviewer}); err != nil {
		t.Fatalf("release of a missing requirement failed: %v", err)
	}
	after, err := w.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("no-op release changed PR state")
	}
}

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
