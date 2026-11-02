package prs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
	"github.com/resident-labs/dyna/engine/store"
)

// feeder keeps what a repository's feeds stored between probes, as the engine's table does.
type feeder struct {
	t    *testing.T
	c    *Client
	rows map[string]store.GitHubFeed
}

func (f *feeder) probe() Probe {
	f.t.Helper()
	if err := f.c.LoadViewer(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	probe, err := f.c.Probe(context.Background(), "owner/repo", f.rows)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, row := range probe.Feeds {
		f.rows[row.Feed] = row
	}
	return probe
}

func started(t *testing.T) (*feeder, *fakegithub.Server, *clock) {
	t.Helper()
	c, server, at := client(t)
	f := &feeder{t: t, c: c, rows: map[string]store.GitHubFeed{}}
	if first := f.probe(); !first.Resync || len(first.Items) != 0 {
		t.Fatalf("a first probe starts at the head: %+v", first)
	}
	return f, server, at
}

func actions(probe Probe) []string {
	var out []string
	for _, item := range probe.Items {
		out = append(out, fmt.Sprintf("%d %s %s", item.Number, item.Action, item.Actor))
	}
	return out
}

func TestAStartedFeedAtRestCostsOnly304s(t *testing.T) {
	c, server, _ := client(t)
	f := &feeder{t: t, c: c, rows: map[string]store.GitHubFeed{}}
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1})
	f.probe()
	graphql, rest, cached := server.Counts()
	for range 5 {
		if probe := f.probe(); len(probe.Items)+len(probe.Feeds)+len(probe.Seen) != 0 || probe.Resync || probe.Moved {
			t.Fatalf("%+v", probe)
		}
	}
	nowGraphQL, nowRest, nowCached := server.Counts()
	if nowGraphQL != graphql || nowRest-rest != 25 || nowCached-cached != 25 {
		t.Fatalf("graphql %d, %d REST reads, %d of them 304", nowGraphQL-graphql, nowRest-rest, nowCached-cached)
	}
}

func TestEachActionHasOneRecordWithItsRealActor(t *testing.T) {
	f, server, _ := started(t)
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1, Author: "writer"})
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 2, Author: "reporter"})
	if got := actions(f.probe()); !slices.Equal(got, []string{"1 opened writer", "2 opened reporter"}) {
		t.Fatalf("%q", got)
	}
	server.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Meta.Labels, p.Meta.Assignees, p.Reviewers = []string{"bug"}, []string{"carol"}, []string{"dave"}
		p.Title = "renamed"
		p.Draft = true
	})
	server.EditIssueAs("bob", "owner/repo", 2, func(i *fakegithub.Issue) {
		i.State = "CLOSED"
		i.Comments = []fakegithub.Comment{{ID: 7, Author: "erin", Body: "hello"}}
	})
	probe := f.probe()
	want := []string{"1 labeled alice", "1 assigned alice", "1 renamed alice", "1 convert_to_draft alice", "1 review_requested alice", "2 closed bob", "2 commented erin"}
	if got := actions(probe); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if len(probe.Seen) != 2 || probe.Seen[1].Title != "renamed" || !probe.Seen[1].Draft || probe.Items[1].Detail != "carol" || probe.Items[4].Detail != "dave" {
		t.Fatalf("%+v", probe)
	}
	if !probe.Items[0].Outside || probe.Items[0].Kind != contract.GitHubPR || probe.Items[6].Kind != contract.GitHubIssue || probe.Items[6].Text != "hello" || probe.Items[6].Author != "reporter" {
		t.Fatalf("%+v", probe.Items)
	}
	if probe := f.probe(); len(probe.Items) != 0 {
		t.Fatalf("told twice: %+v", probe.Items)
	}
}

func TestAMergeIsOneActionAndAPushOnlyMovesTheItem(t *testing.T) {
	f, server, _ := started(t)
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 1})
	f.probe()
	server.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.Head = "new" })
	if probe := f.probe(); len(probe.Items) != 0 || probe.Seen[1] == nil {
		t.Fatalf("%+v", probe)
	}
	server.Edit("owner/repo", 1, func(p *fakegithub.Pull) { p.State = "MERGED" })
	if got := actions(f.probe()); !slices.Equal(got, []string{"1 merged owner-account"}) {
		t.Fatalf("%q", got)
	}
}

func TestDowntimePagesBackAndMissesNothing(t *testing.T) {
	f, server, _ := started(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1})
	f.probe()
	const count = 95
	for n := range count {
		server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) { i.Meta.Labels = append(i.Meta.Labels, fmt.Sprintf("l%d", n)) })
		server.Sleep(time.Minute)
	}
	probe := f.probe()
	if len(probe.Items) != count || probe.Resync {
		t.Fatalf("%d items, resync %v", len(probe.Items), probe.Resync)
	}
	for n, item := range probe.Items {
		if item.Detail != fmt.Sprintf("l%d", n) {
			t.Fatalf("item %d is %+v", n, item)
		}
	}
	if probe := f.probe(); len(probe.Items) != 0 {
		t.Fatalf("%d items again", len(probe.Items))
	}
}

func TestTheIssuesFeedPagesByItsLinkHeader(t *testing.T) {
	f, server, _ := started(t)
	for number := 1; number <= 40; number++ {
		server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: number})
	}
	if probe := f.probe(); len(probe.Items) != 40 {
		t.Fatalf("%d items", len(probe.Items))
	}
}

func TestPagingStopsAtTheLimitAndReportsAGap(t *testing.T) {
	f, server, _ := started(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1})
	f.probe()
	for n := range maxFeedPages*30 + 10 {
		server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) { i.Meta.Labels = []string{fmt.Sprintf("l%d", n)} })
		server.Sleep(time.Minute)
	}
	_, before, _ := server.Counts()
	probe := f.probe()
	if !probe.Resync {
		t.Fatal("no gap reported")
	}
	if _, after, _ := server.Counts(); after-before > 2*maxFeedPages+5 {
		t.Fatalf("%d reads", after-before)
	}
	if probe := f.probe(); probe.Resync || len(probe.Items) != 0 {
		t.Fatalf("the feed stayed lost: %+v", probe)
	}
}

func TestAnItemShownLateIsStillNewAndOnlyOnce(t *testing.T) {
	f, server, _ := started(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1})
	f.probe()
	server.Hold()
	server.EditIssueAs("late", "owner/repo", 1, func(i *fakegithub.Issue) { i.Meta.Labels = []string{"held"} })
	server.Release()
	server.Hold()
	server.EditIssueAs("late", "owner/repo", 1, func(i *fakegithub.Issue) { i.Comments = []fakegithub.Comment{{ID: 9000, Author: "late", Body: "held"}} })
	server.Release()
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 2})
	server.Edit("owner/repo", 2, func(p *fakegithub.Pull) { p.Title = "newer" })
	first := actions(f.probe())
	server.Hold()
	server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) { i.Meta.Labels = []string{"held", "second"} })
	server.Edit("owner/repo", 2, func(p *fakegithub.Pull) { p.Title = "newest" })
	second := actions(f.probe())
	server.Release()
	third := actions(f.probe())
	if !slices.Contains(first, "1 commented late") || slices.Contains(second, "1 labeled x") || !slices.Contains(third, "1 labeled x") {
		t.Fatalf("%q %q %q", first, second, third)
	}
	if again := f.probe(); len(again.Items) != 0 {
		t.Fatalf("told twice: %+v", again.Items)
	}
}

func TestACursorAgesItsSeenItemsOut(t *testing.T) {
	f, server, _ := started(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1})
	f.probe()
	server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) { i.Meta.Labels = []string{"a"} })
	f.probe()
	seen := len(f.rows["events"].Cursor.Seen)
	server.Sleep(2 * feedOverlap)
	server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) { i.Meta.Labels = []string{"b"} })
	f.probe()
	if got := len(f.rows["events"].Cursor.Seen); seen == 0 || got > 2 {
		t.Fatalf("seen %d, then %d", seen, got)
	}
}

func TestAnEditedCommentIsNoNewComment(t *testing.T) {
	f, server, _ := started(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1})
	server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) { i.Comments = []fakegithub.Comment{{ID: 50, Author: "x", Body: "first"}} })
	f.probe()
	server.Sleep(2 * feedOverlap)
	server.EditComment("owner/repo", 50, "edited")
	if probe := f.probe(); len(probe.Items) != 0 {
		t.Fatalf("%+v", probe.Items)
	}
}

func TestInlineCommentsNameTheirPR(t *testing.T) {
	f, server, _ := started(t)
	server.Add(fakegithub.Pull{Repo: "owner/repo", Number: 3})
	f.probe()
	server.EditAs("x", "owner/repo", 3, func(p *fakegithub.Pull) {
		p.Reviews = []fakegithub.Comment{{ID: 1, Author: "x", State: "COMMENTED", Inline: []fakegithub.Comment{{ID: 60, Author: "x", Body: "nit"}}}}
	})
	probe := f.probe()
	if len(probe.Items) != 1 || probe.Items[0].Action != contract.GitHubReviewCommented || probe.Items[0].Number != 3 || probe.Items[0].Kind != contract.GitHubPR {
		t.Fatalf("%+v", probe.Items)
	}
}

func TestTheDefaultBranchFeedHonoursItsPollInterval(t *testing.T) {
	c, server, at := client(t)
	f := &feeder{t: t, c: c, rows: map[string]store.GitHubFeed{}}
	server.SetPollInterval(300)
	f.probe()
	server.SetMain("owner/repo", "moved")
	at.add(time.Minute)
	if probe := f.probe(); probe.Moved {
		t.Fatal("read inside the interval")
	}
	at.add(5 * time.Minute)
	if probe := f.probe(); !probe.Moved || probe.Main != "moved" {
		t.Fatalf("%+v", probe)
	}
}

func TestAnIntervalShorterThanThePassChangesNothing(t *testing.T) {
	c, server, at := client(t)
	f := &feeder{t: t, c: c, rows: map[string]store.GitHubFeed{}}
	server.SetPollInterval(60)
	f.probe()
	server.SetMain("owner/repo", "moved")
	at.add(time.Second)
	if probe := f.probe(); !probe.Moved {
		t.Fatal("a flat pass rested")
	}
}

func TestAMentionReadsAsWhoWroteIt(t *testing.T) {
	f, server, _ := started(t)
	server.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 1, Author: "reporter"})
	f.probe()
	server.EditIssueAs("x", "owner/repo", 1, func(i *fakegithub.Issue) {
		i.Comments = []fakegithub.Comment{{ID: 70, Author: "commenter", Body: "@owner-account"}}
	})
	server.Mention("owner/repo", 1, "owner-account")
	server.Mention("owner/repo", 1, "someone-else")
	probe := f.probe()
	if got := actions(probe); !slices.Equal(got, []string{"1 commented commenter", "1 mentioned commenter"}) || !probe.Items[1].Outside {
		t.Fatalf("%q", got)
	}
	server.Sleep(time.Minute)
	server.Mention("owner/repo", 1, "owner-account")
	if got := actions(f.probe()); !slices.Equal(got, []string{"1 mentioned reporter"}) {
		t.Fatalf("a mention with no comment is the author's: %q", got)
	}
}

func TestParseKeepsWhatARealEventCarries(t *testing.T) {
	data := []byte(`[{"id":34865516769,"event":"assigned","actor":{"login":"gigamans442"},"assignee":{"login":"gigamans442"},"assigner":{"login":"github-actions[bot]"},"created_at":"2026-11-02T01:17:37Z","issue":{"number":6680,"html_url":"https://github.com/o/r/issues/6680","title":"t","user":{"login":"gigamans442"},"state":"open","labels":[],"assignees":[]}},
{"id":34865395132,"event":"labeled","actor":{"login":"ludovic111"},"label":{"name":"run-parity","color":"5319E7"},"created_at":"2026-11-02T01:15:03Z","issue":{"number":6503,"html_url":"https://github.com/o/r/pull/6503","pull_request":{"url":"u","merged_at":null},"user":{"login":"ludovic111"},"state":"open"}},
{"id":34864962794,"event":"review_requested","actor":{"login":"wxundup"},"review_requester":{"login":"wxundup"},"requested_team":{"slug":"core"},"created_at":"2026-11-02T01:06:03Z","issue":{"number":6670,"user":{"login":"wxundup"},"pull_request":{}}},
{"id":34864992644,"event":"subscribed","actor":{"login":"apeirs"},"created_at":"2026-11-02T01:06:40Z","issue":{"number":6667,"user":null}}]`)
	entries, err := parseEvents(data, "o/r", "me")
	if err != nil || len(entries) != 4 {
		t.Fatalf("%+v %v", entries, err)
	}
	assigned, labeled, team, noise := entries[0].item, entries[1].item, entries[2].item, entries[3].item
	if assigned.Actor != "github-actions[bot]" || assigned.Detail != "gigamans442" || assigned.Kind != contract.GitHubIssue {
		t.Fatalf("%+v", assigned)
	}
	if labeled.Detail != "run-parity" || labeled.Kind != contract.GitHubPR || !labeled.Outside {
		t.Fatalf("%+v", labeled)
	}
	if team.Detail != "core" || noise.Action != "" {
		t.Fatalf("%+v %+v", team, noise)
	}
	if !strings.HasPrefix(entries[0].key, "348") {
		t.Fatalf("%+v", entries[0])
	}
}
