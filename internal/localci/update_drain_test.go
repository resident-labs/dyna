package localci

import (
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/jobs"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/updatedrain"
)

func TestUpdateDrainAdmitsOnlyExistingTrialWork(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	root := prstate.Root(dir)
	if err := prstate.Update(ctx, root, func(s *prstate.State) error {
		s.MergeTrials = map[string]*prstate.MergeTrial{"repo": {Branch: "trial/1", Head: "trial-head", Phase: prstate.MergeTrialPhaseRunning}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store := Store{DataDir: dir}
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	release, err := updatedrain.Acquire(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := prstate.Update(ctx, root, func(s *prstate.State) error {
		s.MergeTrials["other"] = &prstate.MergeTrial{Branch: "trial/2", Phase: prstate.MergeTrialPhaseBuilding}
		return nil
	}); !errors.Is(err, updatedrain.ErrDraining) {
		t.Fatalf("new trial: %v", err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "other-head", "node", attachment{RunID: "ordinary"}); !errors.Is(err, updatedrain.ErrDraining) {
		t.Fatalf("ordinary CI: %v", err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "stale-head", "node", attachment{RunID: "stale", Source: jobs.CheckSource{Trial: true}}); !errors.Is(err, updatedrain.ErrDraining) {
		t.Fatalf("stale trial CI: %v", err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "trial-head", "node", attachment{RunID: "trial", Source: jobs.CheckSource{Trial: true}}); err != nil {
		t.Fatalf("admitted trial CI: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "other-head", "node", attachment{RunID: "ordinary"}); err != nil {
		t.Fatalf("CI after release: %v", err)
	}
}

func TestUpdateDrainRecoversDeadInstaller(t *testing.T) {
	ctx := t.Context()
	store := Store{DataDir: t.TempDir()}
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE updater_service(id INTEGER PRIMARY KEY, enabled BOOLEAN NOT NULL); INSERT INTO updater_service VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	_, err = updatedrain.Acquire(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := updatedrain.Suspend(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "head", "node", attachment{RunID: "held"}); !errors.Is(err, updatedrain.ErrDraining) {
		t.Fatalf("deferred lease: %v", err)
	}
	takeover, err := updatedrain.AcquireExisting(ctx, db)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	// Admission stays closed when the retry takes over.
	if err := store.reserveAttachment(ctx, "/repo", "head", "node", attachment{RunID: "gap"}); !errors.Is(err, updatedrain.ErrDraining) {
		t.Fatalf("takeover gap: %v", err)
	}
	if err := takeover(); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO update_drain VALUES(1, -1, 0, ?)`, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "head", "node", attachment{RunID: "held"}); !errors.Is(err, updatedrain.ErrDraining) {
		t.Fatalf("lease after crash: %v", err)
	}
	takeover, err = updatedrain.Acquire(ctx, db)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if err := takeover(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO update_drain VALUES(1, -1, 0, ?)`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "head", "node", attachment{RunID: "recovered"}); err != nil {
		t.Fatalf("expired lease: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE updater_service SET enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := updatedrain.Acquire(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := updatedrain.Suspend(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.reserveAttachment(ctx, "/repo", "other", "node", attachment{RunID: "disabled"}); err != nil {
		t.Fatalf("disabled update retained lease: %v", err)
	}
}
