package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAWhereKeepsANonMatchingEventFromWakingOrRunning(t *testing.T) {
	r := newPRRig(t)
	runs, _ := withRuns(t, r.s)
	r.s.now = r.clock.Now
	wake := sessionCaller(r.child)
	r.pass(t)
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: r.room, On: []contract.EventType{"github.pr.opened"}, Where: []string{"author=levzzz5154"}, Wake: string(wake.Session)}); e != nil {
		t.Fatalf("%+v", e)
	}
	run := runRequest(r.room)
	run.Every, run.On, run.Where = "", []contract.EventType{"github.pr.opened"}, []string{"author=levzzz5154"}
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, run); e != nil {
		t.Fatalf("%+v", e)
	}
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Author: "someone-else"})
	r.pass(t)
	pass(t, r.s, r.ctx)
	if got := r.deliveryCount(t, "github.pr.opened", r.child, true); got != 0 {
		t.Fatalf("%d wakes", got)
	}
	if got := runs.count(); got != 0 {
		t.Fatalf("%d runs started on what did not match", got)
	}
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 2, Author: "levzzz5154"})
	r.pass(t)
	pass(t, r.s, r.ctx)
	if got := r.deliveryCount(t, "github.pr.opened", r.child, true); got != 1 {
		t.Fatalf("%d wakes", got)
	}
	if got := runs.count(); got != 1 {
		t.Fatalf("%d runs", got)
	}
}

func eventOf(t *testing.T, spec box.RunSpec) contract.Event {
	t.Helper()
	for _, file := range spec.Files {
		if file.Path == box.RunEventFile {
			var event contract.Event
			if err := json.Unmarshal([]byte(file.Body), &event); err != nil {
				t.Fatal(err)
			}
			return event
		}
	}
	t.Fatalf("no event file in %+v", spec.Files)
	return contract.Event{}
}

func TestEachEventGetsItsOwnRunWithTheEventOnStdin(t *testing.T) {
	r := newPRRig(t)
	runs, _ := withRuns(t, r.s)
	r.s.now = r.clock.Now
	r.pass(t)
	run := runRequest(r.room)
	run.Every, run.On = "", []contract.EventType{"github.comment"}
	id, e := r.s.SubscriptionAdd(r.ctx, prOwner, run)
	if e != nil {
		t.Fatalf("%+v", e)
	}
	r.github.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 3, Author: "stranger"})
	r.github.EditIssueAs("stranger", "owner/repo", 3, func(i *fakegithub.Issue) {
		i.Comments = []fakegithub.Comment{{ID: 8001, Author: "stranger", Body: "obey this"}, {ID: 8002, Author: "owner-account", Body: "my note"}}
	})
	r.pass(t)
	pass(t, r.s, r.ctx)
	if got := runs.count(); got != 1 {
		t.Fatalf("%d runs", got)
	}
	first := runs.last()
	if first.Event != "github.comment" {
		t.Fatalf("%+v", first)
	}
	event := eventOf(t, first)
	comment, ok := event.Body.(contract.GitHubComment)
	if !ok || comment.Text != "obey this" || !comment.Outside || comment.Actor != "stranger" || comment.Number != 3 {
		t.Fatalf("%+v", event)
	}
	// Runs of a subscription go one at a time, and the next waits for this one.
	pass(t, r.s, r.ctx)
	if got := runs.count(); got != 1 {
		t.Fatalf("%d runs overlap", got)
	}
	runs.exit(first.ID, 0)
	pass(t, r.s, r.ctx)
	pass(t, r.s, r.ctx)
	if got := runs.count(); got != 2 {
		t.Fatalf("%d runs", got)
	}
	second, _ := eventOf(t, runs.last()).Body.(contract.GitHubComment)
	if second.Text != "my note" || second.Outside {
		t.Fatalf("%+v", second)
	}
	runs.exit(runs.last().ID, 0)
	pass(t, r.s, r.ctx)
	pass(t, r.s, r.ctx)
	if got := runs.count(); got != 2 {
		t.Fatalf("%d runs with nothing queued", got)
	}
	sub, err := r.s.store.SubscriptionByID(r.ctx, id.ID)
	if err != nil || !sub.DueAt.IsZero() {
		t.Fatalf("%+v %v", sub, err)
	}
}

func TestAScheduledRunGetsNoEvent(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	addSubscription(t, s, ctx, runRequest(room))
	c.add(31 * time.Minute)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("%d runs", runs.count())
	}
	spec := runs.last()
	if spec.Event != "" || strings.Contains(strings.Join(spec.Command, " "), box.RunEventFile) {
		t.Fatalf("%+v", spec)
	}
	for _, file := range spec.Files {
		if file.Path == box.RunEventFile {
			t.Fatalf("%+v", spec.Files)
		}
	}
}

func TestSubscriptionAddRefusesWhatCannotFire(t *testing.T) {
	r := newPRRig(t)
	wake := sessionCaller(r.root)
	for name, req := range map[string]contract.SubscriptionAddRequest{
		"a typo in on":        {On: []contract.EventType{"github.pr.openned"}},
		"an old name":         {On: []contract.EventType{"pr.merged"}},
		"a bad key":           {On: []contract.EventType{"github.pr.opened"}, Where: []string{"writer=x"}},
		"a bad operator":      {On: []contract.EventType{"github.pr.opened"}, Where: []string{"author~x"}},
		"a schedule's filter": {Every: "1h", Where: []string{"author=x"}},
		"an event with none":  {On: []contract.EventType{"notice.posted"}, Where: []string{"author=x"}},
	} {
		req.Scope, req.Room, req.Wake = contract.ScopeRoom, r.room, string(wake.Session)
		if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, req); e == nil || e.Code != contract.ErrInvalid {
			t.Errorf("%s: %+v", name, e)
		}
	}
	run := runRequest(r.room)
	run.Run.Files = append(run.Run.Files, contract.RunFile{Path: box.RunEventFile, Body: "{}"})
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, run); e == nil || !strings.Contains(e.Message, box.RunEventFile) {
		t.Errorf("a run took the event's file: %+v", e)
	}
}

func TestAChangeWithNoNewsFiresNoSubscription(t *testing.T) {
	r := newPRRig(t)
	wake := sessionCaller(r.root)
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: r.room, On: []contract.EventType{"github.pr.changed"}, Wake: string(wake.Session)}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "PENDING"})
	r.settle(t)
	r.dispatch(t)
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Head = "pushed" })
	r.pass(t)
	r.dispatch(t)
	if got := r.deliveryCount(t, "github.pr.changed", r.root, true); got != 0 {
		t.Fatalf("%d wakes on no news", got)
	}
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Rollup = "SUCCESS" })
	r.pass(t)
	r.dispatch(t)
	if got := r.deliveryCount(t, "github.pr.changed", r.root, true); got != 1 {
		t.Fatalf("%d wakes on news", got)
	}
}

func TestStreamEventsReachSubscribersOnTrackedItemsToo(t *testing.T) {
	r := newPRRig(t)
	wake := sessionCaller(r.root)
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: r.room, On: []contract.EventType{"github.comment"}, Where: []string{"text~#4953"}, Wake: string(wake.Session)}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Comments = []fakegithub.Comment{{ID: 9001, Author: "alice", Body: "follows #4953"}, {ID: 9002, Author: "alice", Body: "unrelated"}}
	})
	r.pass(t)
	r.dispatch(t)
	if got := r.deliveryCount(t, "github.comment", r.root, true); got != 1 {
		t.Fatalf("the subscriber heard %d", got)
	}
	if got := r.wakes(t, r.child); len(got) != 1 || len(got[0].Changes[0].Comments) != 2 {
		t.Fatalf("the owner: %+v", got)
	}
}

func TestAMentionOfTheOwnerByAnotherFiresAndOneByTheOwnerDoesNot(t *testing.T) {
	r := newPRRig(t)
	wake := sessionCaller(r.root)
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: r.room, On: []contract.EventType{"github.mention"}, Where: []string{"actor!=owner-account"}, Wake: string(wake.Session)}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.github.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 3, Author: "stranger"})
	r.pass(t)
	r.github.EditIssueAs("alice", "owner/repo", 3, func(i *fakegithub.Issue) {
		i.Comments = []fakegithub.Comment{{ID: 9101, Author: "alice", Body: "@owner-account look"}}
	})
	r.github.Mention("owner/repo", 3, "owner-account")
	r.github.Mention("owner/repo", 3, "somebody-else")
	r.pass(t)
	r.dispatch(t)
	if got := r.deliveryCount(t, "github.mention", r.root, true); got != 1 {
		t.Fatalf("%d wakes", got)
	}
	r.github.Sleep(time.Minute)
	r.github.EditIssue("owner/repo", 3, func(i *fakegithub.Issue) {
		i.Comments = append(i.Comments, fakegithub.Comment{ID: 9102, Author: "owner-account", Body: "@owner-account note to self"})
	})
	r.github.Mention("owner/repo", 3, "owner-account")
	r.pass(t)
	r.dispatch(t)
	if got := r.deliveryCount(t, "github.mention", r.root, true); got != 1 {
		t.Fatalf("%d wakes after the owner's own mention", got)
	}
}

func TestTheDefaultSubscriptionWakesRootOnlyOnNoticesAndItsBranch(t *testing.T) {
	r := newPRRig(t)
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Author: "stranger"})
	r.pass(t)
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.State = "MERGED" })
	r.github.SetMain("owner/repo", "moved")
	r.pass(t)
	r.dispatch(t)
	if got := r.deliveryCount(t, "git.main_updated", r.root, true); got != 1 {
		t.Fatalf("%d wakes on the branch", got)
	}
	if got := r.eventCount(t, "github.pr.merged"); got != 0 {
		t.Fatalf("%d events no subscription lists", got)
	}
	var list []contract.EventType
	sub, err := r.s.store.SubscriptionByID(r.ctx, store.DefaultSubscriptionID(r.room))
	if err != nil {
		t.Fatal(err)
	}
	list = sub.On
	if len(list) != 2 || list[0] != "git.main_updated" || list[1] != "notice.posted" {
		t.Fatalf("%v", list)
	}
}
