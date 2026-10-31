package watch

import (
	"context"
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

func TestFailedDiscussionRefreshPreservesValidatorsButNotTrust(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.f.Headers = map[string]string{"ETag": `"v1"`}
	e.setCI("success", 1)
	e.write()
	r := e.track()
	e.tick()
	e.f.Pull.Updated = "2026-10-17T12:01:00Z"
	path := "repos/owner/repo/pulls/17/reviews?per_page=100"
	e.f.Raw = map[string]string{path: "HTTP/2.0 504 Gateway Timeout\r\n\r\n{}"}
	e.write()
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("expected failed refresh")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := st.Responses[r.Key]["github.com/"+path]
	if !ok || saved.ETag == "" {
		t.Fatalf("lost validator after failed refresh: %+v", saved)
	}
	// Review assignment must not use the old complete set after a failed refresh.
	e.f.Raw = nil
	e.f.Comments = []Comment{{ID: 99, Kind: "reviews", Association: "COLLABORATOR", User: User{Login: "person"}, Body: "New review"}}
	e.write()
	comments, err := e.s.reviewComments(context.Background(), st, *st.Records[r.Key])
	if err != nil || len(comments) != 1 || comments[0].ID != 99 {
		t.Fatalf("stale review context: %+v, %v", comments, err)
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	gh := e.s.GH
	gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{}, now: e.s.now}
	if _, _, err := gh.request(context.Background(), refRecord(r), path); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(e.calls)
	if !strings.Contains(string(calls), "If-None-Match: ") {
		t.Fatalf("retry omitted validator: %s", calls)
	}
}
