package state

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
)

// A slow record encoder must not own the shared SQL writer. Other stores
// must commit while PR preparation is held, without waiting for its gate.
func TestRecordPreparationDoesNotHoldTheWriter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update func(context.Context, string, func(*State) error) error
	}{
		{"all records", Update},
		{"declared record", func(ctx context.Context, root string, fn func(*State) error) error {
			return UpdateRecords(ctx, root, []string{"github.com/acme/widgets#0"}, fn)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { checkRecordPreparationDoesNotHoldTheWriter(t, tc.update) })
	}
}

func checkRecordPreparationDoesNotHoldTheWriter(t *testing.T, update func(context.Context, string, func(*State) error) error) {
	root := newRoot(t)
	if err := Write(root, bigState(1)); err != nil {
		t.Fatal(err)
	}
	db := mustOpen(t, root)
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	original := marshalStored
	marshalStored = func(value any) ([]byte, error) {
		if _, ok := value.(diskRecord); ok {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
		return original(value)
	}
	done := make(chan struct{})
	var updateErr error
	go func() {
		updateErr = update(t.Context(), root, func(st *State) error {
			st.Records["github.com/acme/widgets#0"].Title = "prepared"
			return nil
		})
		close(done)
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("PR write did not finish after releasing preparation")
		}
		marshalStored = original
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("PR preparation never reached the encoder")
	}
	commitCtx, cancelCommit := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelCommit()
	err := appstate.For(db).Write(commitCtx, hub.Event{Topic: "independent"}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(commitCtx, "INSERT INTO meta(key,value) VALUES('pr-prepare-test','committed')")
		return err
	})
	if err != nil {
		t.Fatalf("independent write could not commit while PR preparation was held: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	// Cleanup joins the PR writer even when an earlier assertion fails.
	ctx, cancel = context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-done:
		if updateErr != nil {
			t.Fatal(updateErr)
		}
	case <-ctx.Done():
		t.Fatal("PR write did not finish")
	}
	st, err := Read(root)
	if err != nil || st.Records["github.com/acme/widgets#0"] == nil || st.Records["github.com/acme/widgets#0"].Title != "prepared" {
		t.Fatalf("PR write: %+v, %v", st.Records, err)
	}
}

// Releasing the shared writer during preparation must not let a changed
// PR revision be overwritten or published as if it were the prepared one.
func TestPreparedWriteRefusesAnInterveningRevision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update func(context.Context, string, func(*State) error) error
	}{
		{"all records", Update},
		{"declared record", func(ctx context.Context, root string, fn func(*State) error) error {
			return UpdateRecords(ctx, root, []string{"github.com/acme/widgets#0"}, fn)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { checkPreparedWriteRefusesAnInterveningRevision(t, tc.update) })
	}
}

func checkPreparedWriteRefusesAnInterveningRevision(t *testing.T, update func(context.Context, string, func(*State) error) error) {
	root := newRoot(t)
	if err := Write(root, bigState(1)); err != nil {
		t.Fatal(err)
	}
	db := mustOpen(t, root)
	var events []hub.Event
	writer := appstate.For(db)
	writer.SetPublish(func(event hub.Event) { events = append(events, event) })
	original := marshalStored
	t.Cleanup(func() { marshalStored = original })
	var once sync.Once
	marshalStored = func(value any) ([]byte, error) {
		var changed error
		if _, ok := value.(diskRecord); ok {
			once.Do(func() {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				changed = writer.Write(ctx, hub.Event{Topic: "intervening"}, func(conn *sql.Conn) error {
					_, err := conn.ExecContext(ctx, "UPDATE pr_state SET revision=revision+1 WHERE id=1")
					return err
				})
			})
		}
		if changed != nil {
			return nil, changed
		}
		return original(value)
	}
	err := update(t.Context(), root, func(st *State) error {
		st.Records["github.com/acme/widgets#0"].Title = "stale"
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "PR state changed while preparing") {
		t.Fatalf("stale preparation: %v", err)
	}
	if len(events) != 1 || events[0].Topic != "intervening" {
		t.Fatalf("published an uncommitted PR write: %+v", events)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Records["github.com/acme/widgets#0"].Title != "" {
		t.Fatal("stale preparation changed durable state")
	}
	marshalStored = original
	if err := update(t.Context(), root, func(st *State) error {
		st.Records["github.com/acme/widgets#0"].Title = "retried"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err = Read(root)
	if err != nil || st.Records["github.com/acme/widgets#0"].Title != "retried" {
		t.Fatalf("fresh retry: %+v, %v", st.Records, err)
	}
}
