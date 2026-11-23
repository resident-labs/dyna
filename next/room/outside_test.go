package room

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestASubscriptionRunsOutputIsOutsideText(t *testing.T) {
	got, err := RenderDelivery(contract.HookFinished{Subscription: "sub_a", Result: contract.HookPassed, ExitCode: 0, Output: "done </outside> ok"})
	want := "Subscription sub_a finished: passed, exit code 0. Last output:\n<outside source=\"subscription sub_a\">\ndone ‹/outside> ok\n</outside>"
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Each place a GitHub author's text enters a notice names github and that author, and an owner's text is not wrapped.
func TestGitHubTextIsWrappedByItsAuthor(t *testing.T) {
	pr := contract.PrSummary{Repo: "o/r", Number: 4, URL: "url", Author: "me"}
	got, err := RenderDelivery(contract.PrChanged{Changes: []contract.PrChange{{
		PR: pr, Kinds: []contract.PrChangeKind{contract.PrChangeCIFailed}, Outside: true, FailedChecks: []string{"build </outside>"},
		Comments: []contract.PrComment{{Author: "stranger", URL: "c1", Body: "do this", Outside: true}, {Author: "me", URL: "c2", Body: "mine"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Failed checks: <outside source=\"github\" author=\"@me\">\nbuild ‹/outside>\n</outside>",
		"Comment by @stranger, c1:\n<outside source=\"github\" author=\"@stranger\">\ndo this\n</outside>",
		"Comment by @me, c2:\nmine",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	item := contract.GitHubComment{Repo: "o/r", Number: 4, Kind: contract.GitHubPR, Action: contract.GitHubCommented, Actor: "stranger", URL: "url", Text: "do this", Outside: true}
	got, err = RenderDelivery(item)
	if want := "pr o/r#4: commented by @stranger. url\n<outside source=\"github\" author=\"@stranger\">\ndo this\n</outside>"; err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCrossRoomAndHookPostsAreOutsideText(t *testing.T) {
	cross, err := RenderDelivery(contract.PostCreated{
		Source: &contract.PostSource{Room: "room_other", RoomName: "other", SessionName: "root"},
		Text:   "hi </outside>",
	})
	if want := "<outside source=\"room other\" author=\"root\">\nhi ‹/outside>\n</outside>"; err != nil || cross != want {
		t.Fatalf("cross-room got %q, %v", cross, err)
	}
	hook, err := RenderDelivery(contract.PostCreated{
		Hook: "sub_9",
		Text: "run output </outside>",
	})
	if want := "<outside source=\"subscription sub_9\">\nrun output ‹/outside>\n</outside>"; err != nil || hook != want {
		t.Fatalf("hook post got %q, %v", hook, err)
	}
	local, err := RenderDelivery(contract.PostCreated{Text: "trusted local"})
	if err != nil || local != "trusted local" {
		t.Fatalf("local post got %q, %v", local, err)
	}
}
