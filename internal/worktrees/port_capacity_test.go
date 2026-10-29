package worktrees

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestObservePortCapacityTracksEveryCrossing(t *testing.T) {
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
