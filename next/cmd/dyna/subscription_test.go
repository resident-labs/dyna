package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSubscriptionScopeTakesExactlyOne(t *testing.T) {
	for _, c := range []struct {
		machine       bool
		room, session string
		want          contract.SubscriptionScope
	}{
		{machine: true, want: contract.ScopeMachine},
		{room: "r", want: contract.ScopeRoom},
		{session: "s", want: contract.ScopeSession},
		{room: "r", session: "s", want: contract.ScopeSession},
	} {
		if got, err := subscriptionScope(c.machine, c.room, c.session); err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	for _, c := range [][3]string{{"", "", ""}, {"m", "r", ""}, {"m", "", "s"}} {
		if _, err := subscriptionScope(c[0] != "", c[1], c[2]); err == nil {
			t.Errorf("%v: accepted", c)
		}
	}
}

func TestRunFilesReadLocalPaths(t *testing.T) {
	local := filepath.Join(t.TempDir(), "watch.sh")
	if err := os.WriteFile(local, []byte("echo hi"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := runFiles([]string{"bin/watch.sh=" + local})
	if err != nil || len(files) != 1 || files[0].Path != "bin/watch.sh" || files[0].Body != "echo hi" {
		t.Fatalf("files: %+v %v", files, err)
	}
	if _, err := runFiles([]string{"no-equals"}); err == nil {
		t.Fatal("accepted a file with no local path")
	}
}

func TestSubscriptionListLine(t *testing.T) {
	next := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	line := describeSubscription(contract.SubscriptionInfo{
		Subscription: contract.Subscription{ID: "sub_a", Scope: contract.ScopeRoom, Every: "30m", Run: &contract.RunSpec{Command: []string{"bash", "watch.sh"}, Image: "horizon"}},
		Failures:     2, Last: contract.HookFailed, NextAt: &next,
	})
	for _, want := range []string{"sub_a", "every 30m", "run horizon: bash watch.sh", "last failed", "2 failures", "next "} {
		if !strings.Contains(line, want) {
			t.Errorf("%q lacks %q", line, want)
		}
	}
}

func TestSubscriptionLineShowsItsFilters(t *testing.T) {
	line := describeSubscription(contract.SubscriptionInfo{Subscription: contract.Subscription{ID: "sub_a", Scope: contract.ScopeRoom, On: []contract.EventType{"github.pr.opened"}, Where: []string{"author=lev", "actor!=x"}}})
	if !strings.Contains(line, "on github.pr.opened where author=lev where actor!=x") {
		t.Fatal(line)
	}
}

func TestSubscriptionEventsListsEachWithItsLine(t *testing.T) {
	var out strings.Builder
	if err := printEventTypes(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"github.pr.opened\ta pull request opened, by a session or on GitHub", "git.main_updated\ta repository's default branch moving", "git.main_ci_finished\tthe CI of a default-branch head finishing, once per head sha even after the branch moved on, with repo, branch, sha, result and runs; where kind is success, failure or cancelled", "github.mention\tthe owner's GitHub account mentioned on an issue or a pull request"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if kind, doc, _ := strings.Cut(line, "\t"); doc == "" {
			t.Errorf("%s has no description", kind)
		}
	}
	if strings.Contains(text, "turn.activity") {
		t.Error("an event no subscription can name is listed")
	}
}
