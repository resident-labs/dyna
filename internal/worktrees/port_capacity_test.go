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

func TestObservePortCapacityAlertsAndRecovers(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	add := func(first, count int) {
		t.Helper()
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
	observe := func() *PortCapacityAlert {
		t.Helper()
		alert, err := ObservePortCapacity()
		if err != nil {
			t.Fatal(err)
		}
		return alert
	}

	add(0, portSlots*9/10-1)
	if alert := observe(); alert != nil {
		t.Fatalf("alert below 90%% = %#v", alert)
	}
	add(portSlots*9/10-1, 1)
	first := observe()
	if first == nil || first.Used != 900 || first.Threshold != 90 || first.Generation != 1 {
		t.Fatalf("90%% alert = %#v; want 900 used, threshold 90, generation 1", first)
	}
	if again := observe(); again != nil {
		t.Fatalf("repeated 90%% alert = %#v; want none", again)
	}

	add(portSlots*9/10, portSlots/20)
	critical := observe()
	if critical == nil || critical.Used != 950 || critical.Threshold != 95 || critical.Generation != first.Generation {
		t.Fatalf("95%% alert = %#v; want one escalation in generation %d", critical, first.Generation)
	}
	if again := observe(); again != nil {
		t.Fatalf("repeated 95%% alert = %#v; want none", again)
	}

	if _, err := db.Exec("DELETE FROM worktree_records WHERE path >= '/capacity/0899'"); err != nil {
		t.Fatal(err)
	}
	if alert := observe(); alert != nil {
		t.Fatalf("recovery alert = %#v; want none", alert)
	}
	add(portSlots*9/10-1, 1)
	recurred := observe()
	if recurred == nil || recurred.Threshold != 90 || recurred.Generation != first.Generation+1 {
		t.Fatalf("alert after recovery = %#v; want generation %d", recurred, first.Generation+1)
	}
}
