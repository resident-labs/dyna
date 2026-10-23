package watch

import (
	"context"
	"strings"
	"testing"
	"time"
)

type keepAllComments struct{}

func (keepAllComments) Triage(_ PRContext, events []Event) []Decision {
	out := make([]Decision, len(events))
	for i := range out {
		out[i] = Decision{Action: "keep", Reason: "custom_keep"}
	}
	return out
}

func TestOutsideCommentHeldUntilExactVersionAdmitted(t *testing.T) {
	e, reviewer := steerEnv(t)
	e.s.Triage = keepAllComments{}
	e.sent = nil
	e.addComment(701, "issue-comments", "untrusted original payload", "outsider")
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	key := "issue-comments:701"
	r, err := (&Store{Root: e.s.Store.Root}).Get(e.record().Key)
	if err != nil {
		t.Fatal(err)
	}
	held, ok := r.HeldComments[key]
	if !ok || held.Body != "untrusted original payload" || held.Version != r.Seen[key] || r.CommentDecisions[key].Action != "hold" {
		t.Fatalf("held comment did not persist: %+v", r.HeldComments)
	}
	assertIsolated := func(payload string) {
		t.Helper()
		for _, notice := range e.sent {
			if strings.Contains(notice.Text, payload) {
				t.Fatalf("outside input reached %s: %+v", notice.Target, notice)
			}
		}
		st, err := e.s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		comments, err := e.s.reviewComments(context.Background(), st, e.record())
		if err != nil {
			t.Fatal(err)
		}
		for _, comment := range comments {
			if strings.Contains(comment.Body, payload) {
				t.Fatalf("outside input reached review context: %+v", comment)
			}
		}
	}
	// A cached keep from older state must never admit outside input.
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].CommentDecisions[key] = Decision{Action: "keep", Reason: "legacy_keep"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertIsolated(held.Body)
	if err := e.s.AdmitComment(r.Key, key); err != nil {
		t.Fatal(err)
	}
	admitted := e.record()
	if _, ok := admitted.HeldComments[key]; ok || admitted.AdmittedComments[key] != held.Version {
		t.Fatalf("admission did not persist exact version: %+v", admitted.AdmittedComments)
	}
	for _, target := range []string{r.Author, reviewer} {
		found := false
		for _, notice := range admitted.Notices {
			if notice.Target == target && strings.Contains(notice.Text, "> "+held.Body) {
				found = true
			}
		}
		if !found {
			t.Fatalf("admitted quoted comment missing for %s: %+v", target, admitted.Notices)
		}
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	comments, err := e.s.reviewComments(context.Background(), st, admitted)
	if err != nil || len(comments) != 1 || !strings.Contains(comments[0].Body, "> "+held.Body) {
		t.Fatalf("admitted version absent from context: %+v, %v", comments, err)
	}
	e.f.Comments[0].Body = "untrusted edited payload"
	e.f.Comments[0].Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.f.Pull.Updated = e.f.Comments[0].Updated
	e.sent = nil
	e.tick()
	edited := e.record()
	if edited.CommentDecisions[key].Action != "hold" || edited.HeldComments[key].Version == held.Version {
		t.Fatalf("admission authorized a later edit: %+v", edited.HeldComments)
	}
	assertIsolated(e.f.Comments[0].Body)
}

func TestCommentTrustSources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event Event
		bots  []string
		want  string
	}{
		{"owner association", Event{Association: "OWNER"}, nil, "keep"},
		{"member association", Event{Association: "MEMBER"}, nil, "keep"},
		{"collaborator association", Event{Association: "COLLABORATOR"}, nil, "keep"},
		{"prior contributor", Event{Association: "CONTRIBUTOR"}, nil, "hold"},
		{"unknown human", Event{Commenter: "person"}, nil, "hold"},
		{"unknown bot", Event{Commenter: "reviewer[bot]", IsBot: true}, nil, "hold"},
		{"coderabbit default", Event{Commenter: "coderabbitai[bot]", IsBot: true}, nil, "keep"},
		{"copilot default", Event{Commenter: "copilot-pull-request-reviewer[bot]", IsBot: true}, nil, "keep"},
		{"short copilot is not default", Event{Commenter: "Copilot", IsBot: true}, nil, "hold"},
		{"explicit bot", Event{Commenter: "reviewer[bot]", IsBot: true}, []string{"reviewer"}, "keep"},
		{"empty list disables defaults", Event{Commenter: "coderabbitai[bot]", IsBot: true}, []string{}, "hold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.event.Text = "Please inspect the parser."
			got := (Rules{TrustedBots: tc.bots}).Triage(PRContext{}, []Event{tc.event})[0]
			if got.Action != tc.want {
				t.Fatalf("decision: %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestAuthenticatedOwnerIsNotPullAuthor(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	e.addComment(801, "issue-comments", "authenticated owner instruction", "account")
	e.addComment(802, "issue-comments", "pull author instruction", "pull-author")
	pull := e.f.Pull
	pull.User.Login = "pull-author"
	notices := newPollNotices(&r)
	e.s.routeComments(&st, &r, Snapshot{Pull: pull, OwnerLogin: "account", Comments: e.f.Comments}, notices)
	if got := r.CommentDecisions["issue-comments:801"]; got.Reason != "owner" || got.Action != "keep" {
		t.Fatalf("authenticated owner not trusted: %+v", got)
	}
	if got := r.CommentDecisions["issue-comments:802"]; got.Action != "hold" {
		t.Fatalf("pull author gained owner trust: %+v", got)
	}
	if got := notices.batches[r.Author]; len(got) != 1 || strings.Contains(got[0], "pull author instruction") {
		t.Fatalf("pull author reached the author session: %+v", got)
	}
}
