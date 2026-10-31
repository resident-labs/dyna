package watch

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestPendingCIDiscussionCacheHasBoundedRefresh(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.f.Checks[0].Status = "in_progress"
	e.addTrustedComment(77, "issue-comments", "Original", "owner")
	e.write()
	e.track()
	e.tick()
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.f.Comments[0].Body = "A note"
	e.tick()
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "/issues/17/comments?") || strings.Contains(string(calls), "/pulls/17/reviews?") || strings.Contains(string(calls), "/pulls/17/comments?") {
		t.Fatalf("unchanged discussion was fetched: %s", calls)
	}
	e.clock = e.clock.Add(9 * time.Minute)
	e.tick()
	if len(e.sent) == 0 || !strings.Contains(e.sent[len(e.sent)-1].Text, "A note") {
		t.Fatal("safety refresh missed unchanged metadata comment")
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.addTrustedComment(78, "comments", "A reply", "owner")
	e.tick()
	calls, err = os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/pulls/17/comments?") || len(e.record().Seen) != 2 {
		t.Fatalf("changed count did not refresh: %s, %v", calls, err)
	}
}
