package view_test

import (
	"strings"
	"testing"

	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

var worktreeComments = []watch.DiffComment{
	{File: "a.go", Line: 3, Quote: "+x := 1", Text: "Name this"},
	{File: "b.go", Line: 9, Quote: " return", Text: "Why?"},
}

func TestWorktreeCommentsWithoutChangePostToTheSession(t *testing.T) {
	e := newEnv(t)
	rec := e.room("wt-plain")
	session := e.start(rec, "worker", nil)
	if err := e.svc.PRs.SendWorktreeComments(ownerCtx, rec.ID, session.ID, worktreeComments); err != nil {
		t.Fatal(err)
	}
	msgs, err := e.svc.Notify.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, m := range msgs {
		if strings.Contains(m.Text, strings.TrimSpace(watch.FormatDiffComments(worktreeComments))) && strings.Contains(m.Text, "a.go:3") && strings.Contains(m.Text, "b.go:9") {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("want one post with both comments, got %d in %+v", found, msgs)
	}
}

func TestWorktreeCommentsWithOpenChangeBecomeOwnerFindings(t *testing.T) {
	e := newEnv(t)
	rec := e.room("wt-change")
	session := e.start(rec, "worker", nil)
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"local": {Key: "local", ChangeID: "change_local", Kind: "local", RoomID: rec.ID, Repo: "owner/repo", Branch: "feature", Head: "head", Dir: session.Dir, Author: session.ID, State: "open", Watching: true},
	}, Reviewers: map[string]watch.Reviewer{}}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	if err := e.svc.PRs.SendWorktreeComments(ownerCtx, rec.ID, session.ID, worktreeComments); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.PRs.Watcher().Store.Get("local")
	if err != nil || len(r.Findings) != 2 || r.Findings[0].Severity != "owner" || len(r.Notices) != 1 || !strings.Contains(r.Notices[0].Text, watch.FormatDiffComments(worktreeComments)) {
		t.Fatalf("record: %+v %v", r, err)
	}
}

func TestWorktreeCommentsGoToTheChangeOfThatWorktree(t *testing.T) {
	e := newEnv(t)
	rec := e.room("wt-two")
	session := e.start(rec, "worker", nil)
	other := t.TempDir()
	open := func(key, dir string) *watch.Record {
		return &watch.Record{Key: key, ChangeID: "change_" + key, Kind: "local", RoomID: rec.ID, Repo: "owner/repo", Branch: key, Head: "head", Dir: dir, Author: session.ID, State: "open", Watching: true}
	}
	save := func(rs ...*watch.Record) {
		st := watch.State{Version: 1, Records: map[string]*watch.Record{}, Reviewers: map[string]watch.Reviewer{}}
		for _, r := range rs {
			st.Records[r.Key] = r
		}
		prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	}
	save(open("a", other), open("b", session.Dir))
	for i := 0; i < 5; i++ { // map order must not matter
		if err := e.svc.PRs.SendWorktreeComments(ownerCtx, rec.ID, session.ID, worktreeComments[:1]); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := e.svc.PRs.Watcher().Store.Get("a")
	b, _ := e.svc.PRs.Watcher().Store.Get("b")
	if len(a.Findings) != 0 || len(b.Findings) != 5 {
		t.Fatalf("findings on a=%d b=%d", len(a.Findings), len(b.Findings))
	}
	save(open("a", session.Dir), open("b", session.Dir))
	if err := e.svc.PRs.SendWorktreeComments(ownerCtx, rec.ID, session.ID, worktreeComments); err == nil || !strings.Contains(err.Error(), "2 open changes") {
		t.Fatalf("ambiguous worktree: %v", err)
	}
}
