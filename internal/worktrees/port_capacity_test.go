package worktrees

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func seedPortRecords(t *testing.T, db *sql.DB, first, count int, paths ...string) {
	t.Helper()
	ctx := context.Background()
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for i := first; i < first+count; i++ {
			path := fmt.Sprintf("/capacity/%04d", i)
			if len(paths) != 0 {
				path = paths[i-first]
			}
			body, err := json.Marshal(Record{Path: path, Port: portMin + i*blockSize})
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO worktree_records(path,run_id,body) VALUES(?,'',?)", path, string(body)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func observePortCapacity(t *testing.T) []*PortCapacityAlert {
	t.Helper()
	alerts, err := ObservePortCapacity()
	if err != nil {
		t.Fatal(err)
	}
	return alerts
}

func stubNoReclaimablePortBlocks(t *testing.T) {
	t.Helper()
	saved := reclaimablePortBlockCount
	reclaimablePortBlockCount = func(context.Context, []Record) int { return 0 }
	t.Cleanup(func() { reclaimablePortBlockCount = saved })
}

// wantAlert describes one pending crossing: threshold and generation.
type wantAlert struct {
	threshold  int
	generation int
}

func assertPending(t *testing.T, alerts []*PortCapacityAlert, want ...wantAlert) {
	t.Helper()
	if len(alerts) != len(want) {
		t.Fatalf("pending alerts = %+v; want %d crossings", alerts, len(want))
	}
	for i, w := range want {
		if alerts[i] == nil || alerts[i].Threshold != w.threshold || alerts[i].Generation != w.generation || alerts[i].Limit != portSlots {
			t.Fatalf("pending alert %d = %+v; want threshold %d, generation %d", i, alerts[i], w.threshold, w.generation)
		}
	}
}

func TestObservePortCapacityCountsReclaimableBlocksAsFree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}

	// These distinct database paths all name the same existing directory.
	// They stay allocated because they have no repository ownership proof.
	root := t.TempDir()
	protected := make([]string, portSlots*9/10-2)
	for i := range protected {
		protected[i] = root + strings.Repeat("/.", i+1)
	}
	protected[0] = root + "/missing"
	seedPortRecords(t, db, 0, len(protected), protected...)

	savedFree, savedPaths, savedCount := portFree, reclaimablePaths, reclaimablePortBlockCount
	t.Cleanup(func() {
		portFree, reclaimablePaths, reclaimablePortBlockCount = savedFree, savedPaths, savedCount
	})
	trees := make([]string, 2)
	for i := range trees {
		trees[i], err = os.MkdirTemp("", "dyna-wt-alert-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(trees[i]) })
	}
	ports := []int{portMin + 898*blockSize, portMin + 899*blockSize}
	for i := range trees {
		if err := Track(Record{Path: trees[i], Repo: "/repo", Port: ports[i]}); err != nil {
			t.Fatal(err)
		}
	}
	scans := 0
	reclaimablePaths = func(_ context.Context, _ string) map[string]bool {
		scans++
		return map[string]bool{trees[0]: true, trees[1]: true}
	}
	busy := [2]bool{}
	busyMissing := false
	portFree = func(port int) bool {
		if busyMissing && port >= portMin && port < portMin+blockSize {
			return false
		}
		for i := range ports {
			if busy[i] && port >= ports[i] && port < ports[i]+blockSize {
				return false
			}
		}
		return true
	}

	// Raw use is 900, but both closed-owner blocks are free: no 90% alert.
	assertPending(t, observePortCapacity(t))
	busy[0] = true
	// One busy block leaves effective use at 899, still below the threshold.
	assertPending(t, observePortCapacity(t))
	busy[1] = true
	// The missing directory still has a reusable block, keeping use at 899.
	assertPending(t, observePortCapacity(t))
	busyMissing = true
	alerts := observePortCapacity(t)
	assertPending(t, alerts, wantAlert{90, 1})
	if alerts[0].Used != 900 {
		t.Fatalf("alert used = %d; want 900 with every reclaimable block socket-protected", alerts[0].Used)
	}
	ack(t, 1, 90)
	busy[1] = false
	assertPending(t, observePortCapacity(t))
	busy[1] = true
	assertPending(t, observePortCapacity(t), wantAlert{90, 2})
	ack(t, 2, 90)

	// Fifty missing paths bring raw use to 950 while effective use stays 900.
	missing := make([]string, 50)
	for i := range missing {
		missing[i] = root + fmt.Sprintf("/missing-release-%02d", i)
	}
	seedPortRecords(t, db, 900, len(missing), missing...)
	current, err := loadRecords(context.Background(), db, "WHERE path=?", protected[1])
	if err != nil || len(current) != 1 {
		t.Fatalf("load release candidate = %#v, %v", current, err)
	}
	// Releasing a protected record leaves raw use at 949 but effective use at
	// 899. The release itself must end the alert period.
	if released, err := releaseRecord(context.Background(), db, current[0], true); err != nil || !released {
		t.Fatalf("releaseRecord() = %v, %v; want a release", released, err)
	}
	replacement := root + strings.Repeat("/.", 1000)
	seedPortRecords(t, db, 950, 1, replacement)
	assertPending(t, observePortCapacity(t), wantAlert{90, 3})
	ack(t, 3, 90)

	// Replace a reclaimable record after measurement but before the release.
	// Raw use stays unchanged, so only the record-set check can reject the
	// stale projection and prevent a duplicate alert generation.
	measure := reclaimablePortBlockCount
	replaced := false
	reclaimablePortBlockCount = func(ctx context.Context, records []Record) int {
		used := measure(ctx, records)
		if !replaced {
			replaced = true
			if err := os.Mkdir(missing[0], 0o700); err != nil {
				t.Fatal(err)
			}
			rec := Record{Path: missing[0], RunID: "protected", Port: portMin + 900*blockSize}
			if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
				return saveRecord(ctx, conn, rec)
			}); err != nil {
				t.Fatal(err)
			}
		}
		return used
	}
	current, err = loadRecords(context.Background(), db, "WHERE path=?", protected[2])
	if err != nil || len(current) != 1 {
		t.Fatalf("load stale-release candidate = %#v, %v", current, err)
	}
	if released, err := releaseRecord(context.Background(), db, current[0], true); err != nil || !released {
		t.Fatalf("stale releaseRecord() = %v, %v; want a release", released, err)
	}
	reclaimablePortBlockCount = measure
	if !replaced {
		t.Fatal("release did not measure effective capacity")
	}
	seedPortRecords(t, db, 951, 1, root+strings.Repeat("/.", 1001))
	assertPending(t, observePortCapacity(t))

	// With only the remaining missing blocks free, allocation must reclaim
	// them in one batch. The active alert period needs one measurement, not
	// one full registry and socket scan per released record.
	freeBlocks := map[int]bool{}
	for i := 1; i < len(missing); i++ {
		freeBlocks[portMin+(900+i)*blockSize] = true
	}
	portFree = func(port int) bool {
		return freeBlocks[port-(port-portMin)%blockSize]
	}
	observations := 0
	reclaimablePortBlockCount = func(ctx context.Context, records []Record) int {
		observations++
		return measure(ctx, records)
	}
	if err := Track(Record{Path: pathsWithBlock(t, portMin, 1)[0]}); err != nil {
		t.Fatalf("Track() with a full pool = %v; want batched reclamation", err)
	}
	if observations != 1 {
		t.Fatalf("release observations = %d; want 1 for the batch", observations)
	}
	if scans != 3 {
		t.Fatalf("ownership scans = %d; want one per observation with a free candidate", scans)
	}
}

func TestObservePortCapacityTracksEveryCrossing(t *testing.T) {
	stubNoReclaimablePortBlocks(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}

	seedPortRecords(t, db, 0, portSlots*9/10-1)
	assertPending(t, observePortCapacity(t))
	seedPortRecords(t, db, portSlots*9/10-1, 1)
	assertPending(t, observePortCapacity(t), wantAlert{90, 1})
	// The crossing stays pending until its delivery is acknowledged.
	assertPending(t, observePortCapacity(t), wantAlert{90, 1})
	ack(t, 1, 90)
	assertPending(t, observePortCapacity(t))

	// An escalation adds its own crossing; both stay pending independently.
	seedPortRecords(t, db, portSlots*9/10, portSlots/20)
	assertPending(t, observePortCapacity(t), wantAlert{95, 1})
	ack(t, 1, 95)
	assertPending(t, observePortCapacity(t))

	// Cleanup frees blocks through the registry's own release path: the period
	// ends without waiting for the next allocation, and the next crossing
	// starts a new generation.
	for i := portSlots*19/20 - 1; i >= portSlots*9/10-1; i-- {
		if err := MarkRemoved(Record{Path: fmt.Sprintf("/capacity/%04d", i), Port: portMin + i*blockSize}); err != nil {
			t.Fatal(err)
		}
	}
	assertPending(t, observePortCapacity(t))
	seedPortRecords(t, db, portSlots*9/10-1, 1)
	assertPending(t, observePortCapacity(t), wantAlert{90, 2})
}

// An undelivered crossing survives a drop in use, an escalation and a later
// period: every crossing is retried until each is acknowledged.
func TestObservePortCapacityKeepsUndeliveredCrossings(t *testing.T) {
	stubNoReclaimablePortBlocks(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}

	// Undelivered through a drop below the band and a new period: the newer
	// crossing of the same threshold supersedes the older one.
	seedPortRecords(t, db, 0, portSlots*9/10)
	assertPending(t, observePortCapacity(t), wantAlert{90, 1})
	for i := 0; i < portSlots/20; i++ {
		if err := MarkRemoved(Record{Path: fmt.Sprintf("/capacity/%04d", i), Port: portMin + i*blockSize}); err != nil {
			t.Fatal(err)
		}
	}
	seedPortRecords(t, db, 0, portSlots/20)
	assertPending(t, observePortCapacity(t), wantAlert{90, 2})

	// Undelivered through an escalation: both thresholds stay pending.
	seedPortRecords(t, db, portSlots*19/20-1, portSlots/20)
	assertPending(t, observePortCapacity(t), wantAlert{90, 2}, wantAlert{95, 2})
	ack(t, 2, 90)
	assertPending(t, observePortCapacity(t), wantAlert{95, 2})
	ack(t, 2, 95)
	assertPending(t, observePortCapacity(t))
}

func ack(t *testing.T, generation, threshold int) {
	t.Helper()
	if err := AckPortAlertDelivery(generation, threshold); err != nil {
		t.Fatal(err)
	}
}
