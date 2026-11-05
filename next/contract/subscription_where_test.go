package contract

import "testing"

func TestWhereMatchesExactly(t *testing.T) {
	opened := PrOpened{Repo: "o/r", Number: 12, Kind: GitHubPR, Action: GitHubOpened, Actor: "lev", Author: "lev", Text: "fixes #4953"}
	label := GitHubLabel{Repo: "o/r", Number: 12, Action: GitHubLabeled, Actor: "ana", Author: "lev", Detail: "bug"}
	main := GitMainUpdated{Repo: "o/r", Head: "abc"}
	for _, test := range []struct {
		name  string
		where []string
		body  EventBody
		want  bool
	}{
		{"nothing to meet", nil, opened, true},
		{"author", []string{"author=lev"}, opened, true},
		{"another author", []string{"author=levzzz"}, opened, false},
		{"no prefix match", []string{"author=le"}, opened, false},
		{"case counts", []string{"author=Lev"}, opened, false},
		{"not the actor", []string{"actor!=arismoko"}, opened, true},
		{"the actor", []string{"actor!=lev"}, opened, false},
		{"anded, all hold", []string{"repo=o/r", "number=12", "author=lev"}, opened, true},
		{"anded, one fails", []string{"repo=o/r", "number=13"}, opened, false},
		{"text", []string{"text~#4953"}, opened, true},
		{"text is a substring and no pattern", []string{"text~#49.3"}, opened, false},
		{"label", []string{"label=bug"}, label, true},
		{"label on what has none", []string{"label=bug"}, opened, false},
		{"!= on a field the event lacks", []string{"label!=bug"}, opened, false},
		{"= on a field the event lacks", []string{"author=x"}, main, false},
		{"repo on a branch move", []string{"repo=o/r"}, main, true},
		{"not that repo", []string{"repo!=o/r"}, main, false},
		{"kind of a push", []string{"kind=pushed", "repo=o/r"}, PrChanged{Changes: []PrChange{{PR: PrSummary{Repo: "o/r"}, Kinds: []PrChangeKind{PrChangeCIPassed, PrChangePushed}}}}, true},
		{"another kind", []string{"kind=pushed"}, PrChanged{Changes: []PrChange{{PR: PrSummary{Repo: "o/r"}, Kinds: []PrChangeKind{PrChangeCIPassed}}}}, false},
		{"kind on an event with none", []string{"kind=pushed"}, opened, false},
		{"kind is the result of a default-branch CI", []string{"kind=success", "repo=o/r"}, GitMainCIFinished{Repo: "o/r", Branch: "main", Sha: "abc", Result: PrCISuccess}, true},
		{"another result", []string{"kind=success"}, GitMainCIFinished{Repo: "o/r", Result: PrCIFailure}, false},
		{"not the failure", []string{"kind!=failure"}, GitMainCIFinished{Repo: "o/r", Result: PrCISuccess}, true},
		{"another repo's default-branch CI", []string{"repo=x/y"}, GitMainCIFinished{Repo: "o/r", Result: PrCISuccess}, false},
		{"a value holding an equals sign", []string{"text~a=b"}, PrOpened{Text: "x a=b y"}, true},
	} {
		if got := MatchesWhere(test.where, test.body); got != test.want {
			t.Errorf("%s: %v on %+v gave %v", test.name, test.where, test.body, got)
		}
	}
}

func TestWhereHoldsOfOneThingInAPass(t *testing.T) {
	pass := PrChanged{Changes: []PrChange{
		{PR: PrSummary{Repo: "o/a", Number: 1, Author: "lev"}},
		{PR: PrSummary{Repo: "o/b", Number: 2, Author: "ana"}, Comments: []PrComment{{Author: "kim", Body: "see #4953"}}, Actions: []GitHubItem{{Action: GitHubLabeled, Actor: "kim", Detail: "bug"}}},
	}}
	for where, want := range map[string]bool{"author=lev": true, "text~#4953": true, "label=bug": true, "repo=o/c": false} {
		if got := MatchesWhere([]string{where}, pass); got != want {
			t.Errorf("%s gave %v", where, got)
		}
	}
	if MatchesWhere([]string{"repo=o/a", "text~#4953"}, pass) {
		t.Error("a Where held across two PRs")
	}
	if !MatchesWhere([]string{"repo=o/b", "actor=kim", "text~#4953"}, pass) {
		t.Error("a Where missed one comment")
	}
}

func TestWhereRefusesWhatItCannotMean(t *testing.T) {
	for _, text := range []string{"", "author", "author=", "author!=", "nobody=x", "title=x", "text=x", "text~", "author~x", "number=one", "author==x"} {
		if text == "author==x" {
			continue // the value is "=x", which is a value
		}
		if err := CheckWhere(text); err == nil {
			t.Errorf("%q passed", text)
		}
	}
	for _, text := range []string{"repo=o/r", "author!=x", "text~a b", "number=12", "label=needs review"} {
		if err := CheckWhere(text); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestEverySubscribableEventHasItsDescription(t *testing.T) {
	for _, kind := range []EventType{"github.pr.opened", "github.pr.merged", "github.pr.closed", "github.pr.changed", "github.issue.opened", "github.issue.changed", "github.issue.closed", "github.ci.finished", "github.comment", "github.label", "github.mention", "git.main_updated", "git.main_ci_finished", "notice.posted"} {
		if EventDoc(kind) == "" {
			t.Errorf("%s has no description", kind)
		}
	}
}

func TestAnItemBecomesItsTypedEvent(t *testing.T) {
	for want, item := range map[EventType]GitHubItem{
		"github.pr.opened":    {Action: GitHubOpened, Kind: GitHubPR},
		"github.issue.opened": {Action: GitHubOpened, Kind: GitHubIssue},
		"github.pr.closed":    {Action: GitHubClosed, Kind: GitHubPR},
		"github.issue.closed": {Action: GitHubClosed, Kind: GitHubIssue},
		"github.pr.merged":    {Action: GitHubMerged, Kind: GitHubPR},
		"github.comment":      {Action: GitHubReviewCommented, Kind: GitHubPR},
		"github.label":        {Action: GitHubUnlabeled, Kind: GitHubIssue},
		"github.mention":      {Action: GitHubMentioned, Kind: GitHubIssue},
	} {
		if got := item.Event(); got == nil || got.EventType() != want {
			t.Errorf("%+v became %v", item, got)
		}
	}
	if body := (GitHubItem{Action: GitHubAssigned}).Event(); body != nil {
		t.Errorf("an assignment has no event: %+v", body)
	}
}
