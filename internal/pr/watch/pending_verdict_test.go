package watch

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPausedWatcherSavesVerdictUntilConfirmedRefresh(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "same head"
		if moved {
			name = "moved head"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			accountGH(t, e)
			e.setCI("success", 1)
			e.track()
			e.tick()
			before := e.record()
			if err := e.s.Store.update(func(st *State) error {
				st.RateLimits = map[string]RateLimit{(RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}, (RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(e.calls)
			err := e.s.Verdict(context.Background(), before.Key, before.Reviewer, before.Head, "approve", "Reviewed this exact head", 0)
			if err == nil || err.Error() != "verdict saved; applies after GitHub refresh" {
				t.Fatalf("paused verdict = %v", err)
			}
			// Force a JSON round trip, as a process restart does.
			data, err := json.Marshal(e.record())
			if err != nil {
				t.Fatal(err)
			}
			var reloaded Record
			if err := json.Unmarshal(data, &reloaded); err != nil {
				t.Fatal(err)
			}
			if err := e.s.Store.update(func(st *State) error {
				st.Records[before.Key].PendingVerdict = reloaded.PendingVerdict
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Retrying the same submission remains a saved result.
			if err := e.s.Verdict(context.Background(), before.Key, before.Reviewer, before.Head, "approve", "Reviewed this exact head", 0); err == nil || err.Error() != "verdict saved; applies after GitHub refresh" {
				t.Fatalf("identical pending submission was rejected: %v", err)
			}
			saved := e.record()
			if saved.Verdict != "" || saved.PendingVerdict == nil || saved.PendingVerdict.Head != before.Head || saved.PendingVerdict.Actor != before.Reviewer {
				t.Fatalf("pending verdict applied without confirmation or not saved: %+v", saved)
			}
			currentCalls, _ := os.ReadFile(e.calls)
			if string(currentCalls) != string(calls) {
				t.Fatal("paused verdict called GitHub")
			}
			e.tick() // A paused tick must not replace the reviewer who already finished.
			pausedRecord := e.record()
			if pausedRecord.Reviewer != before.Reviewer || pausedRecord.PendingVerdict == nil || pausedRecord.Verdict != "" {
				t.Fatalf("paused tick replaced the pending review: %+v", pausedRecord)
			}
			if moved {
				e.f.Pull.Head.SHA = strings.Repeat("b", 40)
			}
			if err := e.s.Store.update(func(st *State) error {
				st.RateLimits = map[string]RateLimit{}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e.tick()
			after := e.record()
			if after.PendingVerdict != nil {
				t.Fatal("pending verdict was not consumed")
			}
			if moved && after.Verdict != "" {
				t.Fatal("old verdict approved a moved head")
			}
			if !moved && (after.Verdict != "approve" || len(after.ReviewRounds) != 1) {
				t.Fatalf("same-head pending verdict not applied once: %+v", after)
			}
		})
	}
}

func TestPendingVerdictStillChecksReviewerAndAssignedHead(t *testing.T) {
	for _, scenario := range []string{"author", "unassigned", "missing head", "wrong head"} {
		t.Run(scenario, func(t *testing.T) {
			e := newEnv(t)
			accountGH(t, e)
			e.setCI("success", 1)
			e.track()
			e.tick()
			r := e.record()
			if err := e.s.Store.update(func(st *State) error {
				st.RateLimits = map[string]RateLimit{(RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}, (RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			actor, head := r.Reviewer, r.Head
			switch scenario {
			case "author":
				actor = r.Author
			case "unassigned":
				actor = "another-reviewer"
			case "missing head":
				head = ""
			case "wrong head":
				head = strings.Repeat("b", 40)
			}
			err := e.s.Verdict(context.Background(), r.Key, actor, head, "approve", "Review complete", 0)
			if err == nil || e.record().PendingVerdict != nil {
				t.Fatalf("invalid review was saved: %v", err)
			}
		})
	}
}

func TestPendingVerdictSurvivesFailedRefreshAndKeepsFindings(t *testing.T) {
	e := newEnv(t)
	accountGH(t, e)
	e.setCI("success", 1)
	e.track()
	e.tick()
	r := e.record()
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits = map[string]RateLimit{(RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}, (RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}).Key(): {PauseUntil: e.clock.Add(time.Hour)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	findings := []Finding{{Severity: "should-fix", File: "parser.go", Line: 3, Why: "Missing bounds check", Fix: "Check the input length"}}
	err := e.s.VerdictWithFindings(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Add the missing check", 0, findings)
	if err == nil || err.Error() != "verdict saved; applies after GitHub refresh" {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits = map[string]RateLimit{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Fail = "/pulls/17"
	e.write()
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if saved := e.record(); saved.PendingVerdict == nil || saved.Verdict != "" {
		t.Fatal("failed refresh consumed or applied the pending verdict")
	}
	e.f.Fail = ""
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits = map[string]RateLimit{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	saved := e.record()
	if saved.PendingVerdict != nil || saved.Verdict != "changes" || saved.Summary != "Add the missing check" ||
		len(saved.Findings) != 1 || saved.Findings[0].Why != findings[0].Why {
		t.Fatalf("pending review payload was lost: %+v", saved)
	}
	e.tick()
	if len(e.record().ReviewRounds) != 1 {
		t.Fatal("saved verdict applied more than once")
	}
}

func TestGitHubFailureWithoutWatcherPauseDoesNotSaveVerdict(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.track()
	e.tick()
	r := e.record()
	e.f.Fail = "/pulls/17"
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Review complete", 0); err == nil {
		t.Fatal("real GitHub error was hidden")
	}
	if e.record().PendingVerdict != nil {
		t.Fatal("non-pause error saved a verdict")
	}
}
