package watch

import (
	"context"
	"dyna-agent/internal/database"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewerQueueReusesSessionAndReplacesHead(t *testing.T) {
	e := newEnv(t)
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{{Name: "judge", MaxConcurrent: 1}}, nil }
	e.setCI("success", 1)
	r := e.track()
	if err := e.s.Store.update(func(st *State) error {
		for _, number := range []int{19, 18} {
			copy := r
			copy.Number = number
			copy.Key = fmt.Sprintf("github.com/owner/repo#%d", number)
			copy.ChangeID = copy.Key
			copy.CI = "passed"
			copy.Title = fmt.Sprint("PR ", number)
			st.Records[copy.Key] = &copy
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Update every record first, as a watcher tick does. No provider starts in this fixture.
	if err := e.s.process(context.Background(), r.Key, Snapshot{Pull: e.f.Pull}, "passed", "pass", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := e.s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := e.s.ReviewerStatus(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Reviewers) != 1 || status.Reviewers[0].Current == nil || status.Reviewers[0].Current.PR != 17 || len(status.Queue) != 2 || status.Queue[0].PR != 18 {
		t.Fatalf("queue order: %+v", status)
	}
	if err := e.s.Store.update(func(st *State) error { st.Records["github.com/owner/repo#18"].Head = "new-head"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.setPullHead(18, "new-head")
	status, _ = e.s.ReviewerStatus(r.RoomID)
	if len(status.Queue) != 2 || status.Queue[0].Head != "new-head" {
		t.Fatalf("queued push: %+v", status.Queue)
	}
	if err := e.s.Store.update(func(st *State) error { st.Records[r.Key].Verdict = "approve"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ = e.s.ReviewerStatus(r.RoomID)
	// The verdict releases PR 17's reviewer, and PR 18 takes the free slot.
	if len(e.launches) != 2 || len(status.Reviewers) != 1 || status.Reviewers[0].Status != "reviewing" || status.Reviewers[0].Current.PR != 18 || len(status.Queue) != 1 {
		t.Fatalf("verdict should free the review slot: %+v launches %d", status, len(e.launches))
	}
}
func TestClosedAndMissingReviewerAreRecreated(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			r := e.track()
			e.tick()
			old := e.record().Reviewer
			if missing {
				delete(e.reviewers, old)
			} else {
				info := e.reviewers[old]
				info.Status = subagent.StatusClosed
				e.reviewers[old] = info
			}
			if err := e.s.dispatchAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := e.record()
			if len(e.launches) != 2 || got.Reviewer == old || got.Reviewer == "" || got.PauseReason != "" {
				t.Fatalf("recovery: %+v", got)
			}
			status, _ := e.s.ReviewerStatus(r.RoomID)
			if len(status.Reviewers) != 1 || status.Reviewers[0].Session == nil || status.Reviewers[0].Session.ID != got.Reviewer {
				t.Fatalf("status: %+v", status)
			}
		})
	}
}
func TestOnlyAssignedReviewerCanRecordVerdict(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	e.tick()
	r = e.record()
	if err := e.s.Verdict(context.Background(), r.Key, "helper-session", r.Head, "approve", "helper findings", 0); err == nil || !strings.Contains(err.Error(), "assigned reviewer") {
		t.Fatalf("helper verdict: %v", err)
	}
	if e.record().Verdict != "" {
		t.Fatal("helper verdict changed the record")
	}
}
func TestReviewAssignmentIsGeneric(t *testing.T) {
	r := Record{Key: "github.com/owner/repo#17", Repo: "owner/repo", Branch: "feature", Base: "base-sha", Head: "head-sha", URL: "https://github.com/owner/repo/pull/17"}
	a := assignment(r)
	if a.ChangeID != r.Key || a.Kind != "github" || a.Repo != r.Repo || a.Branch != r.Branch || a.Base != r.Base || a.Head != r.Head || a.URL != r.URL {
		t.Fatalf("assignment: %+v", a)
	}
	task := reviewTask(r)
	for _, field := range []string{`"changeId":`, `"kind":"github"`, `"branch":"feature"`, `"base":"base-sha"`} {
		if !strings.Contains(task, field) {
			t.Fatalf("assignment task lacks %s", field)
		}
	}
}

func TestProfileChoiceSkipsLatestQuotaFailure(t *testing.T) {
	e := newEnv(t)
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(t.TempDir(), "sessions")}
	e.s.Notify = notify.New(nil, mgr)
	write := func(id, status string, at time.Time) {
		meta := subagent.Meta{ID: id, RoomID: e.author.RoomID, Room: e.author.Room, Profile: "judge", Turns: 1, LastActivity: at}
		recordtest.Write(t, mgr.Store.Root, id, meta)
		turn := subagent.Turn{N: 1, Profile: "judge", Status: status, Created: at, Ended: at, Error: "429 quota exhausted", FailureReason: providerfailure.Quota}
		b, _ := json.Marshal(turn)
		if err := subagent.Migrate(filepath.Dir(mgr.Store.Root)); err != nil {
			t.Fatal(err)
		}
		db, err := database.Open(filepath.Dir(mgr.Store.Root))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES (?,1,(SELECT COALESCE(MAX(revision),0)+1 FROM session_turns),?)", id, b); err != nil {
			t.Fatal(err)
		}
	}
	write("ses_20261017-000001-aaaaaaaa", subagent.TurnError, e.clock.Add(-time.Minute))
	choice, err := e.s.chooseProfile(Reviewer{})
	if err != nil || choice.Name == "judge" {
		t.Fatalf("recent failing profile: %s %v", choice.Name, err)
	}
	write("ses_20261017-000002-bbbbbbbb", subagent.TurnOK, e.clock)
	cached, e2 := e.s.chooseProfile(Reviewer{})
	if e2 != nil || cached.Name == "judge" {
		t.Fatal("profile scan did not reuse its minute cache")
	}
	e.clock = e.clock.Add(time.Minute)
	choice, err = e.s.chooseProfile(Reviewer{})
	if err != nil || choice.Name != "judge" {
		t.Fatalf("newer healthy turn: %s %v", choice.Name, err)
	}
}

// D-68: a signed-out runtime held a review profile out for an hour, and a
// passing live check did not clear the hold. The pool must name the exact
// cause while the hold lasts, and clear it on newer successful evidence.
func TestPinnedProfileUsesNewerHealthyCheck(t *testing.T) {
	e := newEnv(t)
	sol := profile.Profile{Name: "sol", Harness: profile.HarnessMock, Diagnostics: 10, Cost: 8}
	haiku := profile.Profile{Name: "haiku", Harness: profile.HarnessMock, Diagnostics: 7, Cost: 10}
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{sol, haiku}, nil }
	cfg := Reviewer{Profile: "sol", FailedProfiles: map[string]time.Time{"sol (not ready)": e.clock.Add(time.Hour)}}
	if choice, err := e.s.chooseProfile(cfg); err != nil || choice.Name != "haiku" || choice.Cause != rolesession.CauseNotReady || !strings.Contains(choice.Reason, "pinned reviewer profile sol is unavailable") {
		t.Fatalf("held profile: %+v %v", choice, err)
	}
	harnesshealth.New("").RecordProbe(sol, "built-in", harnesshealth.Observation{At: e.clock, Successful: true, StdoutSeen: true, RecognizedRecords: 2}, true)
	if choice, err := e.s.chooseProfile(cfg); err != nil || choice.Name != "sol" || choice.Reason != "pinned by the room" || choice.Cause != "" {
		t.Fatalf("healthy check did not clear the hold: %+v %v", choice, err)
	}
}

func readyReviewPool(t *testing.T, count int) (*testEnv, []string) {
	t.Helper()
	e := newEnv(t)
	e.setCI("success", 1)
	e.write()
	r := e.track()
	keys := []string{r.Key}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].CI = "passed"
		for n := 1; n < count; n++ {
			copy := r
			copy.Number += n
			copy.Key = fmt.Sprintf("github.com/owner/repo#%d", copy.Number)
			copy.ChangeID = copy.Key
			copy.CI = "passed"
			st.Records[copy.Key] = &copy
			keys = append(keys, copy.Key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return e, keys
}

func dispatchReviewPool(t *testing.T, e *testEnv) State {
	t.Helper()
	if err := e.s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReviewerPoolHonorsProfileCap(t *testing.T) {
	for _, cap := range []int{1, 2, 3, 5} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			e, _ := readyReviewPool(t, 4)
			e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{{Name: "judge", MaxConcurrent: cap}}, nil }
			dispatchReviewPool(t, e)
			// Four changes wait, so a cap above four still launches four.
			want := min(cap, 4)
			if len(e.launches) != want {
				t.Fatalf("cap=%d launches=%d", cap, len(e.launches))
			}
		})
	}
}

func TestReviewerPoolWaitsForDependency(t *testing.T) {
	e, keys := readyReviewPool(t, 3)
	if err := e.s.Store.update(func(st *State) error { st.Records[keys[0]].Dependencies = []string{keys[1]}; return nil }); err != nil {
		t.Fatal(err)
	}
	st := dispatchReviewPool(t, e)
	if st.Records[keys[0]].Reviewer != "" || st.Records[keys[1]].Reviewer == "" || st.Records[keys[2]].Reviewer == "" || len(e.launches) != 2 {
		t.Fatalf("dependency order: %+v", st.Records)
	}
	if err := e.s.Store.update(func(st *State) error { st.Records[keys[1]].Verdict = "approve"; return nil }); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	if st.Records[keys[0]].Reviewer == "" || len(e.launches) != 3 {
		t.Fatal("base verdict did not release dependent change")
	}
}

func TestReviewerPoolUsesPerChangeReviewOverrides(t *testing.T) {
	e, keys := readyReviewPool(t, 3)
	if err := e.s.SetReviewEnabled(e.author.RoomID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		for _, key := range keys {
			st.Records[key].PolicyOverride.Review = "on"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st := dispatchReviewPool(t, e)
	if len(e.launches) != 3 {
		t.Fatalf("override pool size: %d", len(e.launches))
	}
	for _, key := range keys {
		if st.Records[key].Reviewer == "" {
			t.Fatal("override-enabled change did not receive review")
		}
	}
}

func TestReviewerPoolRechecksAuthorAndDependenciesAfterStartup(t *testing.T) {
	for _, change := range []string{"author", "dependency"} {
		t.Run(change, func(t *testing.T) {
			e, keys := readyReviewPool(t, 1)
			e.s.LaunchReviewer = func(_ context.Context, r Record, _, _, _ string) (string, error) {
				err := e.s.Store.update(func(st *State) error {
					if change == "author" {
						st.Records[r.Key].Author = "new-reviewer"
					} else {
						base := r
						base.Key = "dependency"
						base.ChangeID = base.Key
						base.CI = "pending"
						st.Records[base.Key] = &base
						st.Records[r.Key].Dependencies = []string{base.Key}
					}
					return nil
				})
				return "new-reviewer", err
			}
			st := dispatchReviewPool(t, e)
			if st.Records[keys[0]].Reviewer != "" {
				t.Fatalf("startup ignored changed %s", change)
			}
		})
	}
}

func reviewState(t *testing.T, e *testEnv, st State, r Record) (string, *int, string) {
	t.Helper()
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	return facts.ReviewState(r)
}
