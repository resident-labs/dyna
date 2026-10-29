package worktrees

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func seedPortRecords(t *testing.T, db *sql.DB, first, count int) {
	t.Helper()
	ctx := context.Background()
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		for i := first; i < first+count; i++ {
			path := fmt.Sprintf("/capacity/%04d", i)
			body := fmt.Sprintf("{\"port\":%d}", portMin+i*blockSize)
			if _, err := conn.ExecContext(ctx, "INSERT INTO worktree_records(path,run_id,body) VALUES(?,'',?)", path, body); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func observePortCapacity(t *testing.T) *PortCapacityAlert {
	t.Helper()
	alert, err := ObservePortCapacity()
	if err != nil {
		t.Fatal(err)
	}
	return alert
}

func TestObservePortCapacityAlertsRecoversAndRetries(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}

	seedPortRecords(t, db, 0, portSlots*9/10-1)
	if alert := observePortCapacity(t); alert != nil {
		t.Fatalf("alert below 90%% = %#v", alert)
	}
	seedPortRecords(t, db, portSlots*9/10-1, 1)
	first := observePortCapacity(t)
	if first == nil || first.Used != 900 || first.Threshold != 90 || first.Generation != 1 {
		t.Fatalf("90%% alert = %#v; want 900 used, threshold 90, generation 1", first)
	}
	if again := observePortCapacity(t); again == nil || again.Threshold != 90 || again.Generation != 1 {
		t.Fatalf("undelivered alert = %#v; want the same crossing until acknowledged", again)
	}
	if err := AckPortAlertDelivery(first.Generation, first.Threshold); err != nil {
		t.Fatal(err)
	}
	if again := observePortCapacity(t); again != nil {
		t.Fatalf("alert after acknowledgement = %#v; want none", again)
	}

	seedPortRecords(t, db, portSlots*9/10, portSlots/20)
	critical := observePortCapacity(t)
	if critical == nil || critical.Used != 950 || critical.Threshold != 95 || critical.Generation != first.Generation {
		t.Fatalf("95%% alert = %#v; want one escalation in generation %d", critical, first.Generation)
	}
	if err := AckPortAlertDelivery(critical.Generation, critical.Threshold); err != nil {
		t.Fatal(err)
	}
	if again := observePortCapacity(t); again != nil {
		t.Fatalf("alert after acknowledgement = %#v; want none", again)
	}

	// Cleanup frees 51 blocks through the registry's own release path. The
	// release rearms the band without waiting for the next allocation.
	for i := portSlots*19/20 - 1; i >= portSlots*9/10-1; i-- {
		if err := MarkRemoved(fmt.Sprintf("/capacity/%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if alert := observePortCapacity(t); alert != nil {
		t.Fatalf("recovery alert = %#v; want none", alert)
	}
	seedPortRecords(t, db, portSlots*9/10-1, 1)
	recurred := observePortCapacity(t)
	if recurred == nil || recurred.Threshold != 90 || recurred.Generation != first.Generation+1 {
		t.Fatalf("alert after cleanup recovery = %#v; want generation %d", recurred, first.Generation+1)
	}
}
