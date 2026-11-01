package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestMainRecheckValidatesCachedPagesPerHeadAndAccount(t *testing.T) {
	s := &Service{}
	r := &Record{Key: "github.com/o/r#1", Host: "github.com", Repo: "o/r", RoomID: "room"}
	var validators int
	gh := GitHub{run: func(_ context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		if strings.Contains(args, "If-None-Match:") {
			validators++
			fmt.Fprint(cmd.Stdout, "HTTP/2.0 304 Not Modified\r\n\r\n")
			return nil
		}
		body := "[]"
		if strings.Contains(args, "/check-runs?") {
			body = `{"check_runs":[{"id":1,"name":"job","status":"completed","conclusion":"success"}]}`
		} else if strings.Contains(args, "/actions/runs?") {
			body = `{"workflow_runs":[]}`
		}
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\nETag: v1\r\n\r\n"+body)
		return nil
	}}
	rollup := &commitRollup{State: "FAILURE"}
	rollup.Contexts.CheckRuns = []rollupStateCount{{State: "CANCELLED", Count: 1}}
	verify := func(sha, account string) {
		t.Helper()
		heads := map[string]headProbe{r.Key: {Account: account, Default: defaultBranchProbe{Head: sha, CI: "FAILURE", Rollup: rollup}}}
		if err := s.recheckCancelledMainCI(t.Context(), gh, []*Record{r}, nil, heads); err != nil {
			t.Fatal(err)
		}
		if got := heads[r.Key].Default.CI; got != "SUCCESS" {
			t.Fatalf("verdict = %s, want SUCCESS", got)
		}
	}
	sha := strings.Repeat("a", 40)
	verify(sha, "alice")
	verify(sha, "alice")
	if validators != 3 {
		t.Fatalf("unchanged head: validated %d pages, want 3", validators)
	}
	verify(strings.Repeat("b", 40), "alice")
	verify(strings.Repeat("b", 40), "bob")
	if validators != 3 {
		t.Fatalf("changed head or account reused validator: %d", validators)
	}
}
