package watch

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSettledDraftKeepsCheapProbeUntilHourlySafetyPoll(t *testing.T) {
	e := newEnv(t)
	e.f.Draft = true
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.setCI("success", 1)
	e.write()
	e.track()
	e.tick()
	before := e.record()
	if !before.IsDraft || !settledCI(before) {
		t.Fatalf("fixture is not a settled draft: draft=%v CI=%s", before.IsDraft, before.CI)
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(10 * time.Minute)
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "\n") != 1 || !strings.Contains(string(calls), "graphql") {
		t.Fatalf("unchanged draft made expensive requests before its hourly poll: %s", calls)
	}
	if got := e.record(); !got.Polling.FullChecked.Equal(before.Polling.FullChecked) {
		t.Fatal("the cheap draft probe rewrote its full observation")
	}
	// The unchanged draft still gets a full safety poll within one hour.
	e.clock = before.Polling.FullChecked.Add(time.Hour)
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.record(); !got.Polling.FullChecked.Equal(e.clock) {
		t.Fatal("draft missed its hourly safety poll")
	}
	// Ready status comes from the minute batch, without waiting another hour.
	e.f.Draft = false
	e.tick()
	if got := e.record(); got.IsDraft || !got.Polling.FullChecked.Equal(e.clock) || len(e.launches) != 1 {
		t.Fatalf("ready transition did not refresh and dispatch: draft=%v checked=%s launches=%d", got.IsDraft, got.Polling.FullChecked, len(e.launches))
	}
	// Active PRs keep their existing ten-minute full safety bound.
	e.clock = e.clock.Add(10 * time.Minute)
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.record(); !got.Polling.FullChecked.Equal(e.clock) {
		t.Fatal("active PR missed its normal safety poll")
	}
}

func TestChangedDraftDoesNotWaitForHourlyPoll(t *testing.T) {
	for _, change := range []string{"head", "CI", "unfinished report", "snapshot error"} {
		t.Run(change, func(t *testing.T) {
			e := newEnv(t)
			e.f.Draft = true
			e.f.Pull.Updated = e.clock.Format(time.RFC3339)
			e.setCI("success", 1)
			e.write()
			e.track()
			e.tick()
			switch change {
			case "head":
				e.f.Pull.Head.SHA = strings.Repeat("b", 40)
			case "CI":
				e.setCI("failure", 2)
			case "unfinished report":
				key := e.record().Key
				if err := e.s.Store.update(func(st *State) error {
					st.Records[key].CITestRetries = map[string]int{"99/1": 0}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "snapshot error":
				key := e.record().Key
				if err := e.s.Store.update(func(st *State) error {
					st.Records[key].Polling.Error = "incomplete observation"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			e.tick()
			got := e.record()
			if !got.Polling.FullChecked.Equal(e.clock) || got.Head != e.f.Pull.Head.SHA || !got.IsDraft || len(e.launches) != 0 {
				t.Fatalf("changed draft was delayed or dispatched: checked=%s head=%s draft=%v launches=%d", got.Polling.FullChecked, got.Head, got.IsDraft, len(e.launches))
			}
		})
	}
}

func TestTickCommentChangeReusesSettledCIAndStillDelivers(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.setCI("success", 1)
	e.f.Checks[0].Completed = e.clock.Format(time.RFC3339)
	e.write()
	e.track()
	e.tick()
	fullChecked := e.record().Polling.FullChecked
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.addComment(88, "issue-comments", "Please check the new note.", "owner")
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "/check-runs?") || strings.Contains(string(calls), "/actions/runs?") || strings.Contains(string(calls), "/status?") {
		t.Fatalf("comment-only change respawned settled CI requests: %s", calls)
	}
	if strings.Count(string(calls), "\n") != 4 || len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "Please check the new note.") {
		t.Fatalf("comment refresh did not use one batch and three discussion requests: calls=%s, sent=%+v", calls, e.sent)
	}
	if got := e.record(); !got.Polling.FullChecked.Equal(fullChecked) {
		t.Fatal("discussion reuse postponed the required full CI safety poll")
	}
	// Changing the head always fetches its own CI evidence.
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.tick()
	calls, err = os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/commits/"+e.f.Pull.Head.SHA+"/check-runs?") || !strings.Contains(string(calls), "/actions/runs?") {
		t.Fatalf("new head reused old CI evidence: %s, %v", calls, err)
	}
}

func TestTickGreenRerunRefreshesCICompletionEvidence(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.setCI("success", 1)
	e.f.Checks[0].Completed = e.clock.Format(time.RFC3339)
	e.write()
	e.track()
	e.tick()
	before := e.record()
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The pull stamp and rollup stay unchanged; only the completed attempt moves.
	e.setCI("success", 2)
	e.f.Checks[0].Completed = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	after := e.record()
	calls, err := os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/check-runs?") || !strings.Contains(string(calls), "/actions/runs?") ||
		after.Polling.CIVersion == before.Polling.CIVersion || len(after.Checks) != 1 || after.Checks[0].ID != 2 {
		t.Fatalf("unchanged green rollup hid a new attempt: probe %s -> %s, checks=%+v, calls=%s, error=%v", before.Polling.CIVersion, after.Polling.CIVersion, after.Checks, calls, err)
	}
}

func TestCommentRefreshDoesNotReuseIncompleteSavedCI(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.setCI("success", 1)
	e.f.Checks[0].Completed = e.clock.Format(time.RFC3339)
	e.write()
	r := e.track()
	e.tick()
	if err := e.s.Store.update(func(st *State) error {
		for path := range st.Responses[r.Key] {
			if strings.Contains(path, "/status?") {
				delete(st.Responses[r.Key], path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.addComment(89, "issue-comments", "A new comment with an incomplete CI cache.", "owner")
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	calls, err := os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/check-runs?") || !strings.Contains(string(calls), "/actions/runs?") || !strings.Contains(string(calls), "/status?") {
		t.Fatalf("partial saved CI bypassed a fresh read: %s, %v", calls, err)
	}
}

// A rollup alone cannot distinguish two successful attempts on the same head.
// If GitHub supplies no completion evidence, a changed pull needs fresh CI.
func TestChangedPullWithoutCompletionEvidenceReadsCurrentAttempt(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Pull.Updated = e.clock.Format(time.RFC3339)
	e.setCI("success", 1)
	e.write()
	e.track()
	e.tick()
	before := e.record()
	if !before.ChecksAt.IsZero() {
		t.Fatal("fixture unexpectedly supplies CI completion evidence")
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.setCI("success", 2)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	after := e.record()
	calls, err := os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/check-runs?") || !strings.Contains(string(calls), "/actions/runs?") ||
		len(after.Checks) != 1 || after.Checks[0].ID != 2 || after.CIKey == before.CIKey {
		t.Fatalf("rollup without completion evidence reused an old attempt: checks=%+v key=%s calls=%s error=%v", after.Checks, after.CIKey, calls, err)
	}
}
