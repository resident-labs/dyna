package hookinstall

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/updaterservice"
)

func TestUpdateBusyWorkflowRuns(t *testing.T) {
	storetest.Isolate(t)
	for _, tc := range []struct {
		status string
		busy   bool
	}{{"running", true}, {"ok", false}, {"error", false}, {"canceled", false}, {"interrupted", false}} {
		runstoretest.Write(t, store.DataDir(), runstore.Meta{ID: "wf_guard", RoomID: "another-room", Status: tc.status})
		busy, err := updateBusy(t.Context())
		if err != nil || busy != tc.busy {
			t.Fatalf("status %s: busy=%v err=%v", tc.status, busy, err)
		}
	}
}

func TestForcedUpdateRecordsRestart(t *testing.T) {
	s, j := fixture(t)
	j.Hook, j.Force = updaterservice.RunName, true
	if err := s.Jobs.Update(j.ID, func(job *Job) error { *job = j; return nil }); err != nil {
		t.Fatal(err)
	}
	var calls []string
	s.Exec = fakeInstaller(t, j, "main", "", &calls)
	if err := s.Locked(t.Context(), j.ID, "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	db, err := s.Jobs.db()
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := db.QueryRowContext(t.Context(), "SELECT last_restart FROM update_restart_guard WHERE id=1").Scan(&last); err != nil || last != s.now().Unix() {
		t.Fatalf("manual restart time = %d, error %v", last, err)
	}
}

func TestUpdateGuardDefersActiveTrialAndCooldown(t *testing.T) {
	storetest.Isolate(t)
	ctx := t.Context()
	s := Service{Jobs: Store{Root: filepath.Join(store.DataDir(), "hooks", "install")}}
	root := prstate.Root(store.DataDir())
	if err := prstate.Update(ctx, root, func(st *prstate.State) error {
		st.MergeTrials = map[string]*prstate.MergeTrial{"repo": {Phase: prstate.MergeTrialPhaseRunning}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if ready, err := s.updateGuard(ctx, now, ""); err != nil || ready {
		t.Fatalf("active trial: ready=%v err=%v", ready, err)
	}
	notices := 0
	s.UpdateNotice = func(string) error { notices++; return nil }
	for _, elapsed := range []time.Duration{3 * time.Hour, 4 * time.Hour} {
		if ready, err := s.updateGuard(ctx, now.Add(elapsed), "repo"); err != nil || ready {
			t.Fatalf("long wait: ready=%v err=%v", ready, err)
		}
	}
	if notices != 1 {
		t.Fatalf("notices = %d, want one", notices)
	}
	if err := prstate.Update(ctx, root, func(st *prstate.State) error {
		st.MergeTrials = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.updateGuard(ctx, now, ""); err != nil || !ready {
		t.Fatalf("quiet: ready=%v err=%v", ready, err)
	}
	if err := s.recordUpdateRestart(ctx, now); err != nil {
		t.Fatal(err)
	}
	if ready, err := s.updateGuard(ctx, now.Add(59*time.Minute), ""); err != nil || ready {
		t.Fatalf("cooldown: ready=%v err=%v", ready, err)
	}
	if ready, err := s.updateGuard(ctx, now.Add(time.Hour), ""); err != nil || !ready {
		t.Fatalf("expired cooldown: ready=%v err=%v", ready, err)
	}
}

func TestQuietCooldownResetsBusyPeriod(t *testing.T) {
	storetest.Isolate(t)
	ctx := t.Context()
	s := Service{Jobs: Store{Root: filepath.Join(store.DataDir(), "hooks", "install")}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root := prstate.Root(store.DataDir())
	setBusy := func(busy bool) {
		t.Helper()
		if err := prstate.Update(ctx, root, func(st *prstate.State) error {
			st.MergeTrials = nil
			if busy {
				st.MergeTrials = map[string]*prstate.MergeTrial{"repo": {Phase: prstate.MergeTrialPhaseRunning}}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	notices := 0
	s.UpdateNotice = func(string) error { notices++; return nil }
	setBusy(true)
	if _, err := s.updateGuard(ctx, now, "repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.recordUpdateRestart(ctx, now); err != nil {
		t.Fatal(err)
	}
	setBusy(true)
	if _, err := s.updateGuard(ctx, now.Add(time.Minute), "repo"); err != nil {
		t.Fatal(err)
	}
	setBusy(false)
	if ready, err := s.updateGuard(ctx, now.Add(2*time.Minute), "repo"); err != nil || ready {
		t.Fatalf("quiet cooldown: %v, %v", ready, err)
	}
	setBusy(true)
	if _, err := s.updateGuard(ctx, now.Add(3*time.Hour), "repo"); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("new busy period inherited old deadline: %d notices", notices)
	}
}
