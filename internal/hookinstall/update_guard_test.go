package hookinstall

import (
	"path/filepath"
	"testing"
	"time"

	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

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
