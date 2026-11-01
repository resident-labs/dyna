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
	} {
		if got, err := subscriptionScope(c.machine, c.room, c.session); err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	for _, c := range [][3]string{{"", "", ""}, {"", "r", "s"}, {"m", "r", ""}} {
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
		Subscription: contract.Subscription{ID: "sub_a", Scope: contract.ScopeRoom, Every: 1800000, Run: &contract.RunSpec{Command: []string{"bash", "watch.sh"}, Image: "horizon"}},
		Failures:     2, Last: contract.HookFailed, NextAt: &next,
	})
	for _, want := range []string{"sub_a", "every 30m0s", "run horizon: bash watch.sh", "last failed", "2 failures", "next "} {
		if !strings.Contains(line, want) {
			t.Errorf("%q lacks %q", line, want)
		}
	}
}
