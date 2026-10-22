package view_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	prview "dyna-agent/internal/pr/view"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// The engine's PR calls act for their caller (requirements item 3). A
// session is refused where its CLI command refuses it, and so is root acting
// in another room; the owner, which a paired device acts as, and the room's
// own root pass. Before, these calls passed a fixed owner actor, so any
// session process could change any room's PR state over the engine socket.
func TestPRCallsActForTheirCaller(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("power-alpha"), e.room("power-beta")
	as := func(role authority.Role, room string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: role, Session: "ses_20261020-000000-0000abcd", Room: room})
	}
	// A workflow worker carries its run's room, as the engine resolves it.
	worker := func(room string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Run: "run_a", Room: room})
	}
	st, _ := e.svc.PRs.Watcher().Store.Read()
	st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: alpha.ID, State: "open", Watching: true, Head: "head", Author: "ses_20261020-000000-0000ffff"}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	policy, err := e.svc.PRs.RoomChangePolicy(alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	refused := func(err error) bool {
		return err != nil && (strings.Contains(err.Error(), " only;") || strings.Contains(err.Error(), "only in its own room"))
	}
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"SetRoomChangePolicy", func(ctx context.Context) error { return e.svc.PRs.SetRoomChangePolicy(ctx, alpha.ID, policy) }},
		{"SetRoomCIBudget", func(ctx context.Context) error {
			return e.svc.PRs.SetRoomCIBudget(ctx, alpha.ID, prview.CIBudgetSettings{CITimeBudget: "warn", CIWarnAfter: "3m", CIFailAfter: "4m", CITestMax: "1s"})
		}},
		{"SetReviewEnabled", func(ctx context.Context) error { return e.svc.PRs.SetReviewEnabled(ctx, alpha.ID, false) }},
		{"SetMergeSettings", func(ctx context.Context) error { return e.svc.PRs.SetMergeSettings(ctx, alpha.ID, 2) }},
		{"SetGitHubAccess", func(ctx context.Context) error { return e.svc.PRs.SetGitHubAccess(ctx, alpha.ID, "read-only") }},
		{"SetReviewPublishing", func(ctx context.Context) error {
			return e.svc.PRs.SetReviewPublishing(ctx, alpha.ID, watch.PublishingLocal)
		}},
		{"ReplyToFinding", func(ctx context.Context) error {
			return e.svc.PRs.ReplyToFinding(ctx, alpha.ID, "change", "bounds", "fixed", "Done")
		}},
		{"SetChangePolicyOverride", func(ctx context.Context) error {
			return e.svc.PRs.SetChangePolicyOverride(ctx, alpha.ID, "change", watch.PolicyOverride{Review: "off", Reason: "test"})
		}},
		{"UntrackPR", func(ctx context.Context) error { return e.svc.PRs.UntrackPR(ctx, alpha.ID, "change") }},
		{"ResumePR", func(ctx context.Context) error { return e.svc.PRs.ResumePR(ctx, alpha.ID, "change") }},
		{"RestartReviewer", func(ctx context.Context) error { return e.svc.PRs.RestartReviewer(ctx, alpha.ID) }},
		{"SetReviewerProfile", func(ctx context.Context) error { return e.svc.PRs.SetReviewerProfile(ctx, alpha.ID, "") }},
		{"SetPRSettings", func(ctx context.Context) error {
			_, err := e.svc.PRs.SetPRSettings(ctx, alpha.ID, prview.PRConfig{})
			return err
		}},
		{"RemovePRNoiseRule", func(ctx context.Context) error { return e.svc.PRs.RemovePRNoiseRule(ctx, alpha.ID, "rule") }},
	} {
		// A worker of the PR's room is refused on its role, exactly as its CLI
		// command refuses it today; a worker of another room is refused too.
		for who, ctx := range map[string]context.Context{"a session that is not the author": as(authority.Session, alpha.ID), "root of another room": as(authority.Root, beta.ID), "a worker of the PR's room": worker(alpha.ID), "a worker of another room": worker(beta.ID)} {
			if err := tc.call(ctx); !refused(err) {
				t.Errorf("%s as %s = %v; want a refusal", tc.name, who, err)
			}
		}
		// The owner and the room's own root pass the checks; ReplyToFinding
		// then fails only because the fixture record has no finding.
		for who, ctx := range map[string]context.Context{"the owner": ownerCtx, "the room's root": as(authority.Root, alpha.ID)} {
			if err := tc.call(ctx); refused(err) {
				t.Errorf("%s as %s = %v", tc.name, who, err)
			}
		}
	}
}

// A lead untracks or resumes its child's PR, by the same rule as the session
// controls: the author or an ancestor of the author's session may, and a
// steward its own tree; an unrelated session and a worker may not.
func TestUntrackAndResumeAllowTheAuthorsAncestor(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("ancestor-alpha")
	parent, author, unrelated := "ses_20261020-000000-00parent", "ses_20261020-000000-00author", "ses_20261020-000000-unrelatd"
	root := e.svc.PRs.Notify.Sessions.Store.Root
	recordtest.Write(t, root, parent, subagent.Meta{ID: parent, Name: "lead", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock"})
	recordtest.Write(t, root, author, subagent.Meta{ID: author, Name: "child", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock", ParentSession: parent})
	recordtest.Write(t, root, unrelated, subagent.Meta{ID: unrelated, Name: "other", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock"})
	st, _ := e.svc.PRs.Watcher().Store.Read()
	st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: alpha.ID, State: "open", Watching: true, Head: "head", Author: author}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	as := func(role authority.Role, session string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: role, Session: session, Room: alpha.ID})
	}
	worker := authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Run: "run_a", Room: alpha.ID})
	for name, call := range map[string]func(context.Context) error{
		"UntrackPR": func(ctx context.Context) error { return e.svc.PRs.UntrackPR(ctx, alpha.ID, "change") },
		"ResumePR":  func(ctx context.Context) error { return e.svc.PRs.ResumePR(ctx, alpha.ID, "change") },
	} {
		for who, ctx := range map[string]context.Context{"the author": as(authority.Session, author), "the author's parent": as(authority.Session, parent), "a steward over the author": as(authority.Steward, parent)} {
			if err := call(ctx); err != nil && strings.Contains(err.Error(), " only;") {
				t.Errorf("%s as %s = %v; want it allowed", name, who, err)
			}
		}
		for who, ctx := range map[string]context.Context{"an unrelated session": as(authority.Session, unrelated), "a worker": worker} {
			if err := call(ctx); err == nil || !strings.Contains(err.Error(), " only;") {
				t.Errorf("%s as %s = %v; want the authority refusal", name, who, err)
			}
		}
	}
}

// The owner's review comments speak for the human owner, so only the owner
// sends them; even the room's own root is refused. Before, any session could
// send them over the engine socket.
func TestReviewCommentsAreTheOwnersOnly(t *testing.T) {
	e := newEnv(t)
	rec := e.room("comments-owner")
	session := e.start(rec, "worker", nil)
	st, _ := e.svc.PRs.Watcher().Store.Read()
	st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: rec.ID, State: "open", Watching: true, Head: "head", Author: session.ID}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	comments := []watch.DiffComment{{File: "a.go", Line: 3, Quote: "+x := 1", Text: "Name this"}}
	calls := map[string]func(context.Context) error{
		"SendDiffComments": func(ctx context.Context) error { return e.svc.PRs.SendDiffComments(ctx, rec.ID, "change", comments) },
		"SendWorktreeComments": func(ctx context.Context) error {
			return e.svc.PRs.SendWorktreeComments(ctx, rec.ID, session.ID, comments)
		},
	}
	as := func(actor authority.Actor) context.Context { return authority.WithActor(context.Background(), actor) }
	for name, call := range calls {
		for who, ctx := range map[string]context.Context{
			"the room's root": as(authority.Actor{Role: authority.Root, Session: "ses_20261020-000000-0000abcd", Room: rec.ID}),
			"the author":      as(authority.Actor{Role: authority.Session, Session: session.ID, Room: rec.ID}),
			"a worker":        as(authority.Actor{Role: authority.Worker, Run: "run_a", Room: rec.ID}),
		} {
			if err := call(ctx); err == nil || !strings.Contains(err.Error(), " only;") {
				t.Errorf("%s as %s = %v; want a refusal", name, who, err)
			}
		}
		if err := call(ownerCtx); err != nil {
			t.Errorf("%s as the owner = %v", name, err)
		}
	}
}

// A restart closes the room's reviewers and then starts one. The end of the
// caller's request, such as a closed browser tab, must not stop it between
// the two and leave the room without a reviewer.
func TestRestartReviewerOutlivesTheRequest(t *testing.T) {
	e := newEnv(t)
	rec := e.room("restart-request")
	started, _, err := e.mgr.Start(ownerCtx, subagent.StartOptions{Goal: "Review", Profile: "mock", Name: "reviewer", Role: "reviewer", Room: rec.Name, Dir: t.TempDir(), Task: "SLEEP:0\nRESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := e.wait(started.ID, 1).ID
	ended, cancel := context.WithCancel(ownerCtx)
	cancel()
	if err := e.svc.PRs.RestartReviewer(ended, rec.ID); err != nil {
		t.Fatalf("restart after the request ended = %v", err)
	}
	if info, err := e.svc.PRs.Notify.Sessions.Store.Info(reviewer); err != nil || info.Status != subagent.StatusClosed {
		t.Fatalf("the old reviewer = %+v, %v; want it closed", info.Status, err)
	}
}

// Transfer, hold, release, close, reply and the policy override act for their
// caller, by the same author rule as untrack and resume: the author, an ancestor of the author's session, a
// steward over it, the owner and the room's root may; an unrelated session,
// a workflow worker and root of another room may not. A refused transfer
// leaves the PR as it was.
func TestTransferAndDraftActForTheirCaller(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("move-alpha"), e.room("move-beta")
	parent, author, unrelated := "ses_20261020-000000-00parent", "ses_20261020-000000-00author", "ses_20261020-000000-unrelatd"
	target := "ses_20261020-000000-00target"
	sessions := e.svc.PRs.Notify.Sessions.Store.Root
	// Transfer needs a stored session, not a completed harness turn.
	recordtest.Write(t, sessions, target, subagent.Meta{ID: target, Name: "next", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock", Resumable: true, HarnessSession: "mock-target"})
	recordtest.Write(t, sessions, parent, subagent.Meta{ID: parent, Name: "lead", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock"})
	recordtest.Write(t, sessions, author, subagent.Meta{ID: author, Name: "child", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock", ParentSession: parent})
	recordtest.Write(t, sessions, unrelated, subagent.Meta{ID: unrelated, Name: "other", RoomID: alpha.ID, Room: alpha.Name, Profile: "mock"})
	reset := func() {
		st, _ := e.svc.PRs.Watcher().Store.Read()
		st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: alpha.ID, State: "open", Watching: true, Head: "head", Author: author}
		st.Records["github.com/owner/repo#1"] = &watch.Record{Key: "github.com/owner/repo#1", Host: "github.com", Repo: "owner/repo", Number: 1, Kind: "github", RoomID: alpha.ID, State: "open", Watching: true, Author: author}
		prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	}
	as := func(role authority.Role, session, room string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: role, Session: session, Room: room})
	}
	worker := func(room string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Run: "run_a", Room: room})
	}
	refused := func(err error) bool {
		return err != nil && (strings.Contains(err.Error(), " only;") || strings.Contains(err.Error(), "only in its own room"))
	}
	calls := map[string]func(context.Context) error{
		"TransferPR": func(ctx context.Context) error {
			_, err := e.svc.PRs.TransferPR(ctx, alpha.ID, "change", "author", target, "")
			return err
		},
		"SetPRDraft hold": func(ctx context.Context) error {
			return e.svc.PRs.SetPRDraft(ctx, alpha.ID, "github.com/owner/repo#1", true, "wait")
		},
		"SetPRDraft release": func(ctx context.Context) error {
			return e.svc.PRs.SetPRDraft(ctx, alpha.ID, "github.com/owner/repo#1", false, "")
		},
		"CloseChange": func(ctx context.Context) error { return e.svc.PRs.CloseChange(ctx, alpha.ID, "change") },
		"ReplyToFinding": func(ctx context.Context) error {
			return e.svc.PRs.ReplyToFinding(ctx, alpha.ID, "change", "f1", "fixed", "Done")
		},
		"SetChangePolicyOverride": func(ctx context.Context) error {
			return e.svc.PRs.SetChangePolicyOverride(ctx, alpha.ID, "change", watch.PolicyOverride{Review: "off", Reason: "test"})
		},
	}
	for name, call := range calls {
		reset()
		before, err := e.svc.PRs.Watcher().Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		for who, ctx := range map[string]context.Context{
			"an unrelated session": as(authority.Session, unrelated, alpha.ID),
			"a worker of the room": worker(alpha.ID),
			"a worker of another":  worker(beta.ID),
			"root of another room": as(authority.Root, "ses_20261020-000000-0000abcd", beta.ID),
		} {
			if err := call(ctx); !refused(err) {
				t.Errorf("%s as %s = %v; want a refusal", name, who, err)
			}
			after, err := e.svc.PRs.Watcher().Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after.Records, before.Records) {
				t.Fatalf("%s as %s changed a record despite refusal", name, who)
			}
		}
		for who, ctx := range map[string]context.Context{
			"the author":                as(authority.Session, author, alpha.ID),
			"the author's parent":       as(authority.Session, parent, alpha.ID),
			"a steward over the author": as(authority.Steward, parent, alpha.ID),
			"the owner":                 ownerCtx,
			"the room's root":           as(authority.Root, "ses_20261020-000000-0000abcd", alpha.ID),
		} {
			reset()
			// A hold or release then fails only at the fake GitHub.
			if err := call(ctx); refused(err) {
				t.Errorf("%s as %s = %v; want it allowed", name, who, err)
			}
		}
	}
	// Only the owner or root resolves a finding; the author and its parent may not.
	for who, ctx := range map[string]context.Context{"the author": as(authority.Session, author, alpha.ID), "the author's parent": as(authority.Session, parent, alpha.ID)} {
		reset()
		if err := e.svc.PRs.ReplyToFinding(ctx, alpha.ID, "change", "f1", "resolved", "Done"); !refused(err) {
			t.Errorf("resolve as %s = %v; want a refusal", who, err)
		}
	}
	reset()
	if _, err := e.svc.PRs.TransferPR(as(authority.Session, unrelated, alpha.ID), alpha.ID, "change", "author", target, ""); !refused(err) {
		t.Fatalf("unrelated transfer = %v", err)
	}
	if r, _ := e.svc.PRs.Watcher().Store.Get("change"); r.Author != author {
		t.Fatalf("a refused transfer moved the author to %s", r.Author)
	}
	got, err := e.svc.PRs.TransferPR(as(authority.Session, parent, alpha.ID), alpha.ID, "change", "author", target, "")
	if err != nil || got.Author != target {
		t.Fatalf("the author's parent transfer = %+v, %v", got.Author, err)
	}
}

// A reply records the replying session for every role that has one, so an
// agent's words never show as the owner's.
func TestReplyRecordsTheReplyingSession(t *testing.T) {
	e := newEnv(t)
	rec := e.room("reply-author")
	const rootSession = "ses_20261020-000000-0000abcd"
	for who, actor := range map[string]authority.Actor{
		rootSession:                    {Role: authority.Root, Session: rootSession, Room: rec.ID},
		"":                             {Role: authority.Owner},
		"ses_20261020-000000-00author": {Role: authority.Session, Session: "ses_20261020-000000-00author", Room: rec.ID},
	} {
		st, _ := e.svc.PRs.Watcher().Store.Read()
		st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: rec.ID, State: "open", Watching: true, Head: "head", Author: "ses_20261020-000000-00author", Findings: []watch.Finding{{ID: "f1"}}}
		prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
		if err := e.svc.PRs.ReplyToFinding(authority.WithActor(context.Background(), actor), rec.ID, "change", "f1", "fixed", "Done"); err != nil {
			t.Fatalf("reply as %v: %v", actor.Role, err)
		}
		r, _ := e.svc.PRs.Watcher().Store.Get("change")
		if replies := r.Findings[0].Replies; len(replies) != 1 || replies[0].Author != who {
			t.Errorf("reply as %v recorded %+v; want author %q", actor.Role, replies, who)
		}
	}
}

// change open --pr acts for its caller: it links a PR only as the caller
// itself, and changing an already tracked PR's notes or policy takes the
// author rule, so the author's parent may and an unrelated session or a
// workflow worker may not.
func TestOpenChangeOnATrackedPRTakesTheAuthorRule(t *testing.T) {
	e, rec, _ := workspaceEnv(t)
	prfixture.FakeGH(t)
	parent, author, unrelated := "ses_20261020-000000-00parent", "ses_20261020-000000-00author", "ses_20261020-000000-unrelatd"
	sessions := e.svc.PRs.Notify.Sessions.Store.Root
	recordtest.Write(t, sessions, parent, subagent.Meta{ID: parent, Name: "lead", RoomID: rec.ID, Room: rec.Name, Profile: "mock"})
	recordtest.Write(t, sessions, author, subagent.Meta{ID: author, Name: "child", RoomID: rec.ID, Room: rec.Name, Profile: "mock", ParentSession: parent})
	recordtest.Write(t, sessions, unrelated, subagent.Meta{ID: unrelated, Name: "other", RoomID: rec.ID, Room: rec.Name, Profile: "mock"})
	const key, url = "github.com/acme/widgets#17", "https://github.com/acme/widgets/pull/17"
	st, _ := e.svc.PRs.Watcher().Store.Read()
	st.Records[key] = &watch.Record{Key: key, Kind: "github", Host: "github.com", Repo: "acme/widgets", Number: 17, URL: url, RoomID: rec.ID, State: "open", Watching: true, Head: "head", Author: author}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	opts := watch.OpenChangeOptions{PolicyOverride: watch.PolicyOverride{Review: "off", Reason: "docs only"}}
	as := func(role authority.Role, session string) context.Context {
		return authority.WithActor(context.Background(), authority.Actor{Role: role, Session: session, Room: rec.ID})
	}
	for who, tc := range map[string]struct {
		ctx     context.Context
		session string
	}{
		"an unrelated session":        {as(authority.Session, unrelated), unrelated},
		"a session naming the author": {as(authority.Session, unrelated), author},
		"a workflow worker":           {authority.WithActor(context.Background(), authority.Actor{Role: authority.Worker, Run: "run_a", Room: rec.ID}), author},
	} {
		if _, err := e.svc.PRs.OpenChange(tc.ctx, rec.ID, tc.session, url, "", opts); err == nil {
			t.Errorf("%s changed the tracked PR", who)
		}
	}
	if r, _ := e.svc.PRs.Watcher().Store.Get(key); r.PolicyOverride.Review != "" {
		t.Fatalf("a refused open changed the policy: %+v", r.PolicyOverride)
	}
	got, err := e.svc.PRs.OpenChange(as(authority.Session, parent), rec.ID, parent, url, "", opts)
	if err != nil || got.Change.Author != author || got.Change.PolicyOverride.Review != "off" {
		t.Fatalf("the author's parent = %+v, %v", got.Change, err)
	}
}

// transferOnActorRead changes the PR after authorPRInRoom has read its record,
// when it reads the authenticated actor. No goroutine or timer controls the race.
type transferOnActorRead struct {
	context.Context
	reads      int
	transferAt int
	transfer   func()
}

func (c *transferOnActorRead) Value(key any) any {
	c.reads++
	if c.reads == c.transferAt {
		c.transfer()
	}
	return c.Context.Value(key)
}

func TestOpenChangeRefusesTransferAfterAuthorRead(t *testing.T) {
	for _, policy := range []bool{false, true} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			e, rec, _ := workspaceEnv(t)
			prfixture.FakeGH(t)
			const author = "ses_20261020-000000-00author"
			const next = "ses_20261020-000000-0000next"
			for _, id := range []string{author, next} {
				recordtest.Write(t, e.svc.PRs.Notify.Sessions.Store.Root, id, subagent.Meta{ID: id, Name: id, RoomID: rec.ID, Room: rec.Name, Profile: "mock"})
			}
			const key = "github.com/acme/widgets#17"
			const url = "https://github.com/acme/widgets/pull/17"
			p := e.svc.PRs.Watcher()
			st, _ := p.Store.Read()
			original := watch.ChangeNotes{Intent: "original"}
			st.Records[key] = &watch.Record{Key: key, Kind: "github", Host: "github.com", Repo: "acme/widgets", Number: 17, URL: url, RoomID: rec.ID, State: "open", Watching: true, Head: "head", Author: author, Notes: original}
			prfixture.SaveState(t, p.Store.Root, st)
			transferred := false
			ctx := &transferOnActorRead{Context: authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: author, Room: rec.ID})}
			ctx.transferAt = 2
			if policy {
				ctx.transferAt = 3
			}
			ctx.transfer = func() {
				st, err := p.Store.Read()
				if err != nil {
					t.Fatal(err)
				}
				st.Records[key].Author = next
				prfixture.SaveState(t, p.Store.Root, st)
				transferred = true
			}
			opts := watch.OpenChangeOptions{Notes: watch.ChangeNotes{Intent: "unauthorized"}}
			if policy {
				opts.PolicyOverride = watch.PolicyOverride{Review: "off", Reason: "docs only"}
			}
			_, err := e.svc.PRs.OpenChange(ctx, rec.ID, author, url, "", opts)
			if !transferred || err == nil || !strings.Contains(err.Error(), "author changed") {
				t.Fatalf("transferred=%v, open error=%v; want stale-author refusal", transferred, err)
			}
			got, err := p.Store.Get(key)
			if err != nil {
				t.Fatal(err)
			}
			if got.Author != next || got.Notes != original || !got.PolicyOverride.Empty() {
				t.Fatalf("open changed the transferred PR: %+v", got)
			}
		})
	}
}

func TestMergeFixPRChecksTheCaller(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("repair-alpha"), e.room("repair-beta")
	p := e.svc.PRs.Watcher()
	st, _ := p.Store.Read()
	st.Records["local"] = &watch.Record{Key: "local", Kind: "local", RoomID: alpha.ID, State: "open", Watching: true}
	prfixture.SaveState(t, p.Store.Root, st)
	for _, actor := range []authority.Actor{
		{Role: authority.Owner},
		{Role: authority.Root, Room: alpha.ID, Session: "root"},
		{Role: authority.Root, Room: beta.ID, Session: "other-root"},
		{Role: authority.Session, Room: alpha.ID, Session: "session"},
		{Role: authority.Worker, Room: alpha.ID, Run: "run"},
	} {
		err := e.svc.PRs.MergeFixPR(authority.WithActor(t.Context(), actor), alpha.ID, "local")
		allowed := actor.Role == authority.Owner || actor.Role == authority.Root && actor.Room == alpha.ID
		reachedStore := err != nil && strings.Contains(err.Error(), "requires an open tracked GitHub PR")
		if err == nil || reachedStore != allowed {
			t.Errorf("merge-fix as %+v = %v", actor, err)
		}
	}
}
