package watch

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBotCommentFixtures(t *testing.T) {
	var fixtures []struct {
		Name, Source, Action string
		Event                Event
	}
	b, err := os.ReadFile("testdata/bot-comments.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			f.Event.Association = "COLLABORATOR"
			decision := (Rules{}).Triage(PRContext{OwnerLogin: "owner"}, []Event{f.Event})[0]
			if decision.Action != f.Action {
				t.Fatalf("%s: %+v, want %s", f.Source, decision, f.Action)
			}
		})
	}
}

func TestRuleTriage(t *testing.T) {
	for _, tc := range []struct {
		name, body, login string
		bot               bool
		action, reason    string
	}{
		{"owner", "Rate limit exceeded", "owner", true, "keep", "owner"},
		{"author", Marker("ses_author") + " reply", "owner", false, "keep", "owner"},
		{"reviewer", Marker("ses_reviewer") + " fix parser", "owner", false, "keep", "owner"},
		{"known rate limit", "Rate limit exceeded", "coderabbitai[bot]", true, "drop", "known_rate_limit_noise"},
		{"placeholder", "Review in progress", "coderabbitai[bot]", true, "drop", "known_review_placeholder"},
		{"known bot useful", "bounds issue", "coderabbitai[bot]", true, "keep", "possibly_useful_feedback"},
		{"noise and real content", "Rate limit exceeded\nBounds bug at line 7", "coderabbitai[bot]", true, "keep", "possibly_useful_feedback"},
		{"unknown bot", "needs inspection", "new-bot[bot]", true, "hold", "outside_commenter"},
		{"human", "please fix", "other", false, "hold", "outside_commenter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := (Rules{}).Triage(PRContext{AuthorSession: "ses_author", OwnerLogin: "owner"}, []Event{{Text: tc.body, Commenter: tc.login, IsBot: tc.bot}})[0]
			if d.Action != tc.action || d.Reason != tc.reason {
				t.Fatalf("decision=%+v", d)
			}
		})
	}
}
func TestDuplicateFilterKeepsInlineLocationsAndOwnerComments(t *testing.T) {
	events := []Event{{Association: "COLLABORATOR", Kind: "issue-comments", Text: "same finding", Commenter: "bot[bot]", IsBot: true}, {Association: "COLLABORATOR", Kind: "issue-comments", Text: "same finding", Commenter: "bot[bot]", IsBot: true}, {Association: "COLLABORATOR", Kind: "comments", Text: "same inline finding", Commenter: "bot[bot]", IsBot: true}, {Association: "COLLABORATOR", Kind: "comments", Text: "same inline finding", Commenter: "bot[bot]", IsBot: true}, {Text: "owner note", Commenter: "owner", IsBot: true}, {Text: "owner note", Commenter: "owner", IsBot: true}}
	d := (Rules{}).Triage(PRContext{OwnerLogin: "owner"}, events)
	for i, want := range []string{"keep", "drop", "keep", "keep", "keep", "keep"} {
		if d[i].Action != want {
			t.Fatalf("decision %d=%+v", i, d[i])
		}
	}
}

type captureTriage struct {
	batches [][]Event
	invalid bool
}

func (t *captureTriage) Triage(_ PRContext, events []Event) []Decision {
	t.batches = append(t.batches, append([]Event(nil), events...))
	if t.invalid {
		return nil
	}
	d := make([]Decision, len(events))
	for i := range d {
		d[i] = Decision{Action: "drop", Reason: "test_filter"}
	}
	return d
}
func TestBatchTriagePreservesOrderAndOwnerWake(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	triage := &captureTriage{}
	e.s.Triage = triage
	e.addComment(21, "issue-comments", "first owner note", "owner")
	e.addComment(22, "issue-comments", "second owner note", "owner")
	notices := newPollNotices(&r)
	e.s.routeComments(&st, &r, Snapshot{OwnerLogin: "owner", Comments: e.f.Comments}, notices)
	if len(triage.batches) != 1 || len(triage.batches[0]) != 2 || !strings.Contains(triage.batches[0][0].ID, "21") || !strings.Contains(triage.batches[0][1].ID, "22") {
		t.Fatalf("batch order: %+v", triage.batches)
	}
	if got := notices.batches[r.Author]; len(got) != 2 || !strings.Contains(got[0], "first owner note") || !strings.Contains(got[1], "second owner note") {
		t.Fatalf("filter dropped owner comments: %+v", got)
	}
	holdOutsideComments(t)
	triage.invalid = true
	e.addComment(23, "issue-comments", "unknown human", "other")
	e.s.routeComments(&st, &r, Snapshot{OwnerLogin: "owner", Comments: e.f.Comments}, notices)
	if len(notices.batches[r.Author]) != 2 || r.CommentDecisions["issue-comments:23"].Action != "hold" {
		t.Fatalf("invalid batch did not hold unknown feedback: %+v", notices.batches)
	}
}

func TestExactCodeRabbitMarkersDoNotHideRealReviewWrappers(t *testing.T) {
	for _, tc := range []struct{ body, action string }{
		{"<!-- This is an auto-generated comment: summarize by coderabbit.ai -->\n<!-- This is an auto-generated comment: rate limited by coderabbit.ai -->\nrate-limit boilerplate", "drop"},
		{"<!-- This is an auto-generated comment: skip review by coderabbit.ai -->\nskip-review boilerplate", "drop"},
		{rateLimitedReply, "drop"},
		{strings.Replace(rateLimitedReply, "<details>", "<!-- CodeRabbit review command invocation: 932ae08f-291d-4b09-b31c-6132f60071fb -->\n<details>", 1), "drop"},
		{rateLimitedReply + "\nReal finding: the caller can pass a negative length.", "keep"},
		{"<!-- This is an auto-generated reply by CodeRabbit -->\nReal finding: add a bounds check.", "keep"},
		{"<!-- This is an auto-generated comment: summarize by coderabbit.ai -->\nReal finding: the parser reads past the end.", "keep"},
	} {
		d := (Rules{}).Triage(PRContext{OwnerLogin: "owner"}, []Event{{Commenter: "coderabbitai[bot]", IsBot: true, Text: tc.body}})[0]
		if d.Action != tc.action {
			t.Fatalf("%q: %+v", tc.body, d)
		}
	}
}

func TestCodeRabbitQuotaAutofixFooter(t *testing.T) {
	body, err := os.ReadFile("testdata/coderabbit-quota-footer.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body, kind, login, want string }{
		{"actual quota notice", string(body), "issue-comments", "coderabbitai[bot]", "drop"},
		{"appended finding", string(body) + "\nBug: the parser can leak credentials at server.go:12", "issue-comments", "coderabbitai[bot]", "keep"},
		{"inline comment", string(body), "comments", "coderabbitai[bot]", "keep"},
		{"owner", string(body), "issue-comments", "owner", "keep"},
		{"changed control", strings.Replace(string(body), "Autofix</strong>", "Autofix</strong> bug in parser.go:12", 1), "issue-comments", "coderabbitai[bot]", "keep"},
		{"code in control", strings.Replace(string(body), "Autofix</strong>", "Autofix</strong> ```123```", 1), "issue-comments", "coderabbitai[bot]", "keep"},
		{"nonzero findings", string(body) + "\nFindings: 2", "issue-comments", "coderabbitai[bot]", "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := (Rules{}).Triage(PRContext{OwnerLogin: "owner"}, []Event{{Text: tc.body, Kind: tc.kind, Commenter: tc.login, IsBot: true}})[0]
			if d.Action != tc.want {
				t.Fatalf("%+v, want %s", d, tc.want)
			}
		})
	}
}

func TestCodeRabbitQuotaFooterFakeGH(t *testing.T) {
	body, err := os.ReadFile("testdata/coderabbit-quota-footer.md")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Checks, e.f.Runs = nil, nil
	e.track()
	e.addComment(101, "issue-comments", string(body), "coderabbitai[bot]")
	e.tick()
	if len(e.sent) != 0 || e.record().PendingDrops["known_bot_noise_marker"] != 1 {
		t.Fatalf("quota notice woke author: %+v %+v", e.sent, e.record())
	}
	e.addComment(102, "issue-comments", string(body)+"\nBug at parser.go:12: ensure bounds.", "coderabbitai[bot]")
	e.tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "Bug at parser.go:12") || !strings.Contains(e.sent[0].Text, "known_bot_noise_marker: 1") {
		t.Fatalf("lost finding or filter audit: %+v", e.sent)
	}
}

func TestGenericBotNoiseKeepsProtectedFeedback(t *testing.T) {
	for _, tc := range []struct {
		name, body, kind, login string
		bot                     bool
		want                    string
	}{
		{"unknown quota", "Rate limit exceeded", "issue-comments", "other-reviewer[bot]", true, "drop"},
		{"unknown progress", "Review in progress…", "reviews", "other-reviewer[bot]", true, "drop"},
		{"human progress", "Review in progress", "issue-comments", "human", false, "keep"},
		{"inline quota", "Rate limit exceeded", "comments", "other-reviewer[bot]", true, "keep"},
		{"inline progress", "Review in progress", "comments", "coderabbitai[bot]", true, "keep"},
		{"appended finding", "Review in progress\nBug at parser.go:12", "reviews", "other-reviewer[bot]", true, "keep"},
		{"unknown wrapper", "<!-- This is an auto-generated comment: skip review by coderabbit.ai -->", "issue-comments", "other-reviewer[bot]", true, "keep"},
		{"code", "Rate limit exceeded\n```go\nreturn nil\n```", "reviews", "other-reviewer[bot]", true, "keep"},
		{"nonzero findings", "Review in progress\nFindings: 2", "reviews", "other-reviewer[bot]", true, "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (Rules{}).Triage(PRContext{}, []Event{{Association: "COLLABORATOR", Text: tc.body, Kind: tc.kind, Commenter: tc.login, IsBot: tc.bot}})[0]
			if got.Action != tc.want {
				t.Fatalf("decision: %+v, want %s", got, tc.want)
			}
		})
	}
}
