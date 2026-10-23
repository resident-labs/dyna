package state

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
)

// Issue #247: A holds the PR lock in Update and waits for the writer, while
// B's committed write publishes an event whose handler reads PR state. The
// handler runs after B releases the writer, so A commits and both finish.
// The timer only bounds the failure: the fixed path never waits on it.
func TestPublishReadingPRStateDoesNotBlockUpdate(t *testing.T) {
	root := newRoot(t)
	w := appstate.For(mustOpen(t, root))
	publishing, read := make(chan struct{}), make(chan error, 1)
	w.SetPublish(func(ev hub.Event) {
		if ev.Topic != "localci" {
			return
		}
		close(publishing)
		_, err := ReadSnapshot(root)
		read <- err
	})
	locked, updated := make(chan struct{}), make(chan error, 1)
	go func() {
		updated <- Update(context.Background(), root, func(st *State) error {
			close(locked)
			<-publishing
			st.Records["k"] = &Record{Key: "k", RoomID: "room_1", State: "open"}
			return nil
		})
	}()
	<-locked
	wrote := make(chan error, 1)
	go func() {
		wrote <- w.Write(context.Background(), hub.Event{Topic: "localci", Key: "result:abc"}, func(*sql.Conn) error { return nil })
	}()
	deadlock := time.NewTimer(10 * time.Second)
	defer deadlock.Stop()
	for _, done := range []chan error{updated, read, wrote} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadlock.C:
			t.Fatal("deadlock: Update waits for the writer while a publish under it waits for the PR lock")
		}
	}
	w.WaitPublished()
}
