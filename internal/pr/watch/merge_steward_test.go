package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

type batchSteward struct {
	starts int
	err    error
	report string
}

func (f *batchSteward) Profiles() ([]profile.Profile, error) {
	return []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}, nil
}
func (f *batchSteward) Failures(time.Time) (map[string]time.Time, error) { return nil, nil }
func (f *batchSteward) Inspect(string) (subagent.Info, subagent.Turn, error) {
	return subagent.Info{}, subagent.Turn{}, errors.New("missing")
}
func (f *batchSteward) Turn(string, int) (subagent.Turn, error) {
	return subagent.Turn{}, errors.New("missing")
}
func (f *batchSteward) Named(string) (subagent.Info, error) {
	return subagent.Info{}, errors.New("missing")
}
func (f *batchSteward) Start(_, _, _, _, task string) (subagent.Info, int, error) {
	f.starts++
	f.report = task
	return subagent.Info{}, 1, f.err
}
func (f *batchSteward) Assign(context.Context, string, string) (int, error) {
	return 0, errors.New("unexpected assign")
}
func (f *batchSteward) Close(context.Context, string) error { return errors.New("unexpected close") }

func TestFailedBatchAssignsSteward(t *testing.T) { testFailedBatchAssignment(t, false) }
func TestFailedBatchStewardStartFailureWakesRoot(t *testing.T) {
	testFailedBatchAssignment(t, true)
}

func testFailedBatchAssignment(t *testing.T, fail bool) {
	t.Helper()
	name := "root"
	if !fail {
		name = "steward"
	}
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	f := &batchSteward{}
	if fail {
		f.err = errors.New("provider start failed")
	}
	e.s.StewardSessions = f
	r := Record{Key: "change", RoomID: e.author.RoomID, Dir: e.author.Dir}
	b := state.MergeTrial{Branch: "trial/1", Head: "tested", Phase: "failed", RootReport: "TestBroken failed", Changes: []state.MergeTrialChange{{Key: r.Key}}}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{"repo": &b}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.stopMergeBatch(t.Context(), "repo", r, b); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := saved.MergeTrials["repo"]
	if !got.Notified || got.Notifier != name || saved.MergePauses[r.RoomID] == "" {
		t.Fatalf("trial = %+v", got)
	}
	wantStarts := 1
	if f.starts != wantStarts || !strings.Contains(f.report, "TestBroken failed") {
		t.Fatalf("assignment = %+v", f)
	}
	wantNotices := 0
	if name == "root" {
		wantNotices = 1
	}
	if fail && len(e.sent) == 1 && !e.sent[0].NeedsRoot {
		t.Fatal("failed steward start did not mark root action")
	}
	if len(e.sent) != wantNotices {
		t.Fatalf("notices = %+v", e.sent)
	}
	restarted := *e.s
	if err := restarted.stopMergeBatch(t.Context(), "repo", r, *got); err != nil {
		t.Fatal(err)
	}
	if f.starts != wantStarts || len(e.sent) != wantNotices {
		t.Fatal("restart repeated assignment")
	}
}

// merge.stewardProfile off skips the steward entirely: a failed batch goes
// straight to the root notice, and the queue pauses exactly as before.
func TestFailedBatchStewardOffWakesRoot(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	f := &batchSteward{}
	e.s.StewardSessions = f
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(settings.KeyMergeStewardProfile, "off"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	r := Record{Key: "change", RoomID: e.author.RoomID, Dir: e.author.Dir}
	b := state.MergeTrial{Branch: "trial/1", Head: "tested", Phase: "failed", RootReport: "TestBroken failed", Changes: []state.MergeTrialChange{{Key: r.Key}}}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{"repo": &b}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.stopMergeBatch(t.Context(), "repo", r, b); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := saved.MergeTrials["repo"]
	if !got.Notified || got.Notifier != "root" || saved.MergePauses[r.RoomID] == "" {
		t.Fatalf("trial = %+v", got)
	}
	if f.starts != 0 {
		t.Fatalf("steward started %d times with the profile off", f.starts)
	}
	if len(e.sent) != 1 || !e.sent[0].NeedsRoot {
		t.Fatalf("notices = %+v", e.sent)
	}
	restarted := *e.s
	if err := restarted.stopMergeBatch(t.Context(), "repo", r, *got); err != nil {
		t.Fatal(err)
	}
	if f.starts != 0 || len(e.sent) != 1 {
		t.Fatal("restart repeated the notice")
	}
}
