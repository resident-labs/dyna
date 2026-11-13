package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	for _, want := range []string{"github.pr.opened\ta pull request opened, by a session or on GitHub", "git.main_updated\ta repository's default branch moving", "git.main_ci_finished\tthe CI of a default-branch head settling at the first failed check or when all checks finish otherwise, once per head sha even after the branch moved on, with repo, branch, sha, result and runs known at that moment; where kind is success, failure or cancelled", "github.mention\tthe owner's GitHub account mentioned on an issue or a pull request"} {
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

func TestSubscriptionWorkflowDescription(t *testing.T) {
	for _, key := range []string{"", "{repo}#{number}"} {
		sub := contract.Subscription{Workflow: &contract.WorkflowSubscription{Script: "export const meta = {name: 'review', profiles: ['sol']}\n"}, Key: key}
		line := describeSubscription(contract.SubscriptionInfo{Subscription: sub})
		want := "workflow review"
		if key != "" {
			want += " per " + key
		}
		if !strings.Contains(line, want+"\t") {
			t.Fatal(line)
		}
	}
}
func TestSubscriptionFollowUpRequests(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		method, body string
	}{
		{[]string{"fire", "sub"}, "fire", "{\"id\":\"sub\"}"},
		{[]string{"fire", "sub", "--key", "", "--event", "{\"type\":\"hook.due\",\"body\":{}}"}, "fire", "{\"id\":\"sub\",\"key\":\"\",\"event\":{\"type\":\"hook.due\",\"body\":{}}}"},
		{[]string{"update", "sub", "--latest=false", "--key", "", "--parent", "", "--where", "kind=pushed,opened"}, "update", "{\"id\":\"sub\",\"latest\":false,\"key\":\"\",\"parent\":\"\",\"where\":[\"kind=pushed,opened\"]}"},
		{[]string{"update", "sub", "--where", ""}, "update", `{"id":"sub","where":[]}`},
		{[]string{"state", "sub"}, "state", "{\"id\":\"sub\"}"},
		{[]string{"state", "sub", "--key", "", "--value", "null"}, "state", "{\"id\":\"sub\",\"key\":\"\",\"value\":null}"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			called := false
			c := &socketClient{http: &http.Client{Transport: guidanceTransport(func(r *http.Request) (*http.Response, error) {
				called = true
				if r.URL.Path != "/v1/subscription."+tc.method {
					t.Fatal(r.URL.Path)
				}
				var got, want any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %#v want %#v", got, want)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
			})}}
			cmd := subscriptionCommand(func() *socketClient { return c })
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("no call")
			}
		})
	}
}
func TestSubscriptionFollowUpRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{{"fire", "sub", "--event", "{"}, {"update", "sub", "--args", "{}"}, {"state", "sub", "--value", "null"}, {"state", "sub", "--key", "k", "--value", "{"}} {
		cmd := subscriptionCommand(func() *socketClient { t.Fatal("unexpected request"); return nil })
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
