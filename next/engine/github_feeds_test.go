package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/room"
)

func TestAnUnchangedPassMakesOnly304sAndNoGraphQL(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.track(t, fakegithub.Issue{Repo: "owner/repo", Number: 2})
	r.clock.add(11 * time.Minute)
	r.pass(t)
	graphql, rest, cached := r.github.Counts()
	r.pass(t)
	nowGraphQL, nowRest, nowCached := r.github.Counts()
	if nowGraphQL != graphql || nowRest-rest != 5 || nowCached-cached != 5 {
		t.Fatalf("%d queries, %d reads, %d of them 304", nowGraphQL-graphql, nowRest-rest, nowCached-cached)
	}
}

func TestEachActionReportsOnceWithItsActor(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) { p.Meta.Labels = []string{"bug"} })
	r.github.EditAs("bob", "owner/repo", 1, func(p *fakegithub.Pull) { p.Meta.Assignees = []string{"carol"} })
	r.github.EditAs("dave", "owner/repo", 1, func(p *fakegithub.Pull) { p.State = "CLOSED" })
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 1 {
		t.Fatalf("wakes %+v", got)
	}
	text, err := room.RenderDelivery(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := "PR owner/repo#1: labels changed by @alice; assigned @carol by @bob; closed without merging by @dave. https://github.com/owner/repo/pull/1\n" + room.WrapExternal("alice", "labels +bug"); text != want {
		t.Fatalf("%q", text)
	}
	r.pass(t)
	r.connect()
	r.clock.add(11 * time.Minute)
	r.pass(t)
	if got := r.wakes(t, r.child); len(got) != 1 {
		t.Fatalf("told again: %+v", got)
	}
	if got := r.eventCount(t, "pr.closed"); got != 1 {
		t.Fatalf("%d pr.closed events", got)
	}
}

func TestARestartMidFeedRepeatsNothingAndLosesNothing(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Comments = []fakegithub.Comment{{ID: 5000, Author: "alice", Body: "first"}}
	})
	r.pass(t)
	r.connect()
	r.pass(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Comments = append(p.Comments, fakegithub.Comment{ID: 5001, Author: "alice", Body: "while down"})
	})
	r.connect()
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 2 || got[0].Changes[0].Comments[0].Body != "first" || got[1].Changes[0].Comments[0].Body != "while down" {
		t.Fatalf("wakes %+v", got)
	}
}

func TestDowntimePagesBackAndTellsEverythingInOneDelivery(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	for n := range 70 {
		r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) { p.Meta.Labels = append(p.Meta.Labels, fmt.Sprintf("l%d", n)) })
		r.github.Sleep(time.Minute)
	}
	r.connect()
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 1 || len(got[0].Changes[0].Actions) != 70 {
		t.Fatalf("wakes %+v", got)
	}
	if pr, err := r.s.store.PR(r.ctx, "owner/repo", 1); err != nil || len(pr.Meta.Labels) != 70 {
		t.Fatalf("%+v %v", pr.Meta, err)
	}
}

func TestAReopenAfterCloseIsCaught(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) { p.State = "CLOSED" })
	r.pass(t)
	if pr, err := r.s.store.PR(r.ctx, "owner/repo", 1); err != nil || pr.State != contract.PrClosed {
		t.Fatalf("%+v %v", pr, err)
	}
	r.clock.add(11 * time.Minute)
	r.pass(t)
	graphql, _, _ := r.github.Counts()
	r.pass(t)
	if now, _, _ := r.github.Counts(); now != graphql {
		t.Fatal("a closed PR was read")
	}
	r.github.EditAs("bob", "owner/repo", 1, func(p *fakegithub.Pull) { p.State = "OPEN" })
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 2 || got[1].Changes[0].PR.State != contract.PrOpen || got[1].Changes[0].Actions[0].Action != contract.GitHubReopened || got[1].Changes[0].Actions[0].Actor != "bob" {
		t.Fatalf("wakes %+v", got)
	}
	if pr, err := r.s.store.PR(r.ctx, "owner/repo", 1); err != nil || pr.State != contract.PrOpen {
		t.Fatalf("%+v %v", pr, err)
	}
	held, err := r.s.store.Responsibilities(r.ctx, r.child)
	if err != nil || held.Count(contract.ResponsibilityPR) != 1 {
		t.Fatalf("a reopened PR holds its session again: %+v %v", held, err)
	}
}

func TestAReopenIsCaughtOnARepositoryWithNothingElseWatched(t *testing.T) {
	r := newPRRig(t)
	r.github.AddIssue(fakegithub.Issue{Repo: "stranger/repo", Number: 1})
	if _, e := r.s.IssueTrack(r.ctx, prOwner, contract.IssueTrackRequest{Repo: "stranger/repo", Number: 1, Session: r.child}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.settle(t)
	r.github.EditIssueAs("alice", "stranger/repo", 1, func(i *fakegithub.Issue) { i.State = "CLOSED" })
	r.pass(t)
	r.clock.add(11 * time.Minute)
	r.pass(t)
	r.github.EditIssueAs("bob", "stranger/repo", 1, func(i *fakegithub.Issue) { i.State = "OPEN" })
	r.pass(t)
	got := r.issueWakes(t, r.child)
	if len(got) != 2 || got[1][0].Issue.State != contract.IssueOpen || got[1][0].Actions[0].Actor != "bob" {
		t.Fatalf("wakes %+v", got)
	}
}

func TestWhatHappensOnAClosedItemStaysOutOfItsOwnersWay(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.State = "MERGED" })
	r.pass(t)
	r.clock.add(11 * time.Minute)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Comments = []fakegithub.Comment{{ID: 6000, Author: "alice", Body: "late thanks"}}
	})
	r.pass(t)
	if got := r.wakes(t, r.child); len(got) != 1 {
		t.Fatalf("wakes %+v", got)
	}
}

func TestCIPendingStaysReadUntilItFinishes(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Rollup: "PENDING"})
	r.settle(t)
	for range 5 {
		before := len(r.github.QuerySizes())
		r.pass(t)
		if sizes := r.github.QuerySizes()[before:]; len(sizes) != 1 || sizes[0] != 1 {
			t.Fatalf("queries %v while CI ran", sizes)
		}
		r.clock.add(time.Minute)
	}
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Rollup = "SUCCESS" })
	r.pass(t)
	if got := r.wakes(t, r.child); len(got) != 1 || got[0].Changes[0].Kinds[0] != contract.PrChangeCIPassed {
		t.Fatalf("wakes %+v", got)
	}
	graphql, _, _ := r.github.Counts()
	r.pass(t)
	if now, _, _ := r.github.Counts(); now != graphql {
		t.Fatal("a finished PR stayed read")
	}
}

func TestAPushIsWatchedForTheChecksItStarts(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	// GitHub has not added the checks yet, so the first read shows none, and no feed will say when they arrive.
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Head = "pushed" })
	r.pass(t)
	r.clock.add(time.Minute)
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Rollup = "FAILURE"; p.FailedChecks = []string{"build"} })
	r.pass(t)
	if got := r.wakes(t, r.child); len(got) != 1 || got[0].Changes[0].Kinds[0] != contract.PrChangeCIFailed || got[0].Changes[0].FailedChecks[0] != "build" {
		t.Fatalf("wakes %+v", got)
	}
}

func TestADefaultBranchMoveRefreshesConflicts(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 2})
	r.settle(t)
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Mergeable, p.MergeState = "UNKNOWN", "UNKNOWN" })
	r.github.Edit("owner/repo", 2, func(p *fakegithub.Pull) { p.Mergeable, p.MergeState = "CONFLICTING", "DIRTY" })
	graphql, _, _ := r.github.Counts()
	r.pass(t)
	if now, _, _ := r.github.Counts(); now != graphql {
		t.Fatal("a conflict moved no feed, so nothing should read yet")
	}
	r.github.SetMain("owner/repo", "moved")
	before := len(r.github.QuerySizes())
	r.pass(t)
	if sizes := r.github.QuerySizes()[before:]; len(sizes) != 1 || sizes[0] != 2 {
		t.Fatalf("queries %v", sizes)
	}
	got := r.wakes(t, r.child)
	if len(got) != 1 || len(got[0].Changes) != 1 || got[0].Changes[0].PR.Number != 2 || got[0].Changes[0].Kinds[0] != contract.PrChangeConflict {
		t.Fatalf("wakes %+v", got)
	}
	// GitHub had not worked PR 1 out, so it is read until it says.
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Mergeable, p.MergeState = "CONFLICTING", "DIRTY" })
	before = len(r.github.QuerySizes())
	r.pass(t)
	if sizes := r.github.QuerySizes()[before:]; len(sizes) != 1 || sizes[0] != 1 {
		t.Fatalf("queries %v", sizes)
	}
	if got := r.wakes(t, r.child); len(got) != 2 || got[1].Changes[0].PR.Number != 1 {
		t.Fatalf("wakes %+v", got)
	}
}

func TestOutsideTextIsWrappedByTheRealActor(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1, Title: "mine"})
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 2, Author: "stranger", Title: "theirs"})
	if _, e := r.s.PrTrack(r.ctx, sessionCaller(r.child), contract.PrTrackRequest{Number: 2}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.settle(t)
	// An outsider's label on the owner's PR is outside text. The owner's label on an outsider's PR is not.
	r.github.EditAs("maintainer", "owner/repo", 1, func(p *fakegithub.Pull) { p.Meta.Labels = []string{"obey me"}; p.Title = "and me" })
	r.github.Edit("owner/repo", 2, func(p *fakegithub.Pull) { p.Meta.Labels = []string{"triaged"} })
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 1 {
		t.Fatalf("wakes %+v", got)
	}
	text, err := room.RenderDelivery(got[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"labels changed, title changed by @maintainer", room.WrapExternal("maintainer", "labels +obey me\ntitle and me"), "labels +triaged by @owner-account"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s\nlacks %q", text, want)
		}
	}
	status, e := r.s.PrStatus(r.ctx, sessionCaller(r.child), contract.PrStatusRequest{Number: 1})
	if e != nil || status.PR.Title != room.WrapExternal("maintainer", "and me") {
		t.Fatalf("%+v %+v", status, e)
	}
}

func TestUntrackedItemsAreQuietEventsForSubscribers(t *testing.T) {
	r := newPRRig(t)
	r.pass(t)
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 9, Author: "stranger", Meta: fakegithub.Meta{Body: "see #4953"}})
	r.pass(t)
	if got := r.eventCount(t, "github.item"); got != 0 {
		t.Fatalf("%d events with no subscriber", got)
	}
	wake := sessionCaller(r.root)
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: r.room, On: []contract.EventType{"github.item"}, Wake: &wake}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.github.EditAs("alice", "owner/repo", 9, func(p *fakegithub.Pull) { p.Meta.Labels = []string{"bug"} })
	r.pass(t)
	if got := r.eventCount(t, "github.item"); got != 1 {
		t.Fatalf("%d events", got)
	}
	var body []byte
	if err := r.s.store.Read().QueryRow("SELECT body FROM events WHERE type='github.item'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var item contract.GitHubItem
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	if item.Action != contract.GitHubLabeled || item.Actor != "alice" || item.Author != "stranger" || item.Detail != "bug" || item.Kind != contract.GitHubPR || !item.Outside {
		t.Fatalf("%+v", item)
	}
	if got := r.deliveryCount(t, "github.item", r.child, true) + r.deliveryCount(t, "github.item", r.child, false); got != 0 {
		t.Fatalf("%d deliveries", got)
	}
	r.dispatch(t)
	if got := r.deliveryCount(t, "github.item", r.root, true); got != 1 {
		t.Fatalf("the subscriber heard %d", got)
	}
}

func TestDeployOnExistingStateReplaysNothing(t *testing.T) {
	r := newPRRig(t)
	wake := sessionCaller(r.root)
	if _, e := r.s.SubscriptionAdd(r.ctx, prOwner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: r.room, On: []contract.EventType{"github.item"}, Wake: &wake}); e != nil {
		t.Fatalf("%+v", e)
	}
	// Rows that a poll has read, with years of feed behind them and no cursor.
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Author: "stranger"})
	if _, e := r.s.PrTrack(r.ctx, sessionCaller(r.child), contract.PrTrackRequest{Number: 1}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.github.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 2})
	if _, e := r.s.IssueTrack(r.ctx, sessionCaller(r.child), contract.IssueTrackRequest{Number: 2}); e != nil {
		t.Fatalf("%+v", e)
	}
	for n := range 40 {
		r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
			p.Meta.Labels = []string{fmt.Sprint(n)}
			p.Comments = append(p.Comments, fakegithub.Comment{ID: int64(7000 + n), Author: "alice", Body: "old"})
		})
	}
	r.pass(t)
	r.clock.add(11 * time.Minute)
	r.pass(t)
	if got := r.wakes(t, r.child); len(got) != 0 {
		t.Fatalf("wakes %+v", got)
	}
	if got := r.issueWakes(t, r.child); len(got) != 0 {
		t.Fatalf("issue wakes %+v", got)
	}
	if got := r.eventCount(t, "github.item"); got != 0 {
		t.Fatalf("%d events replayed", got)
	}
	// The next news reads back past the first page, and still only the new is news.
	r.github.EditAs("bob", "owner/repo", 1, func(p *fakegithub.Pull) { p.Meta.Labels = []string{"new"} })
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 1 || len(got[0].Changes[0].Actions) != 2 || got[0].Changes[0].Actions[0].Detail != "new" || got[0].Changes[0].Actions[1].Detail != "39" || got[0].Changes[0].Actions[1].Actor != "bob" {
		t.Fatalf("wakes %+v", got)
	}
}

func TestAClosePassedByTheFeedsIsReadOnTheFirstPass(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.track(t, fakegithub.Issue{Repo: "owner/repo", Number: 2})
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.ctx, "DELETE FROM github_feeds")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.github.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.State = "MERGED" })
	r.github.EditIssue("owner/repo", 2, func(i *fakegithub.Issue) { i.State = "CLOSED" })
	r.pass(t)
	if got := r.wakes(t, r.child); len(got) != 1 || got[0].Changes[0].Kinds[0] != contract.PrChangeMerged || got[0].Issues[0].Kinds[0] != contract.PrChangeClosed {
		t.Fatalf("wakes %+v", got)
	}
}
