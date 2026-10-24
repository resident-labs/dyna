package state

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

// A close must drain a publish that is already running, including the changes
// queued behind it, because what a publish reads may close next: the database
// its writer came from. TestWriterHealth failed this way in CI, when its
// cleanup closed the database while a queued change still published.
//
// The channels force the order: the first publish is held, so the second change
// must queue for the background publisher, and the close must not return until
// both have published.
func TestWriterCloseDrainsPublishing(t *testing.T) {
	w, _ := testWriter(t)
	firstStarted, firstRelease := make(chan struct{}), make(chan struct{})
	secondStarted, secondRelease := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var published []string
	topics := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(published)
	}
	w.SetPublish(func(c hub.Event) {
		switch c.Topic {
		case "first":
			close(firstStarted)
			<-firstRelease
		case "second":
			close(secondStarted)
			<-secondRelease
		}
		mu.Lock()
		published = append(published, c.Topic)
		mu.Unlock()
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- w.Write(context.Background(), hub.Event{Topic: "first"}, insertThing) }()
	<-firstStarted
	// The first change is still publishing, so this one queues for the
	// background publisher and Write returns before it is published.
	if err := w.Write(context.Background(), hub.Event{Topic: "second"}, insertThing); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		w.Close()
		close(closed)
	}()
	// Let the close reach its wait before the publishes move on.
	runtime.Gosched()
	close(firstRelease)
	<-secondStarted
	select {
	case <-closed:
		t.Fatal("Close returned while a queued change was still publishing")
	default:
	}
	close(secondRelease)
	<-closed
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := topics(); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("published %v; want the running change, then the queued one", got)
	}
	// A change that commits after the close publishes nothing, so a later
	// caller cannot reach a closed handle through this Writer.
	if err := w.Write(context.Background(), hub.Event{Topic: "after-close"}, insertThing); err != nil {
		t.Fatal(err)
	}
	if got := topics(); slices.Contains(got, "after-close") {
		t.Fatalf("published %v after Close; a closed Writer publishes nothing", got)
	}
}

// Closing the database drains a publish first, so a publish that reads the
// database, as a store's publish does, never reads a closed handle. This is the
// ordering the failing test hit: its cleanup closed the database under a
// publish that was still running.
func TestDatabaseCloseDrainsPublishing(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(dir) })
	if _, err := db.Exec("CREATE TABLE things(name TEXT)"); err != nil {
		t.Fatal(err)
	}
	// The database's registered Writer, which is the one a close drains.
	w := For(db)
	started, release := make(chan struct{}), make(chan struct{})
	var readErr error
	w.SetPublish(func(hub.Event) {
		close(started)
		<-release
		var rows int
		if err := db.QueryRow("SELECT COUNT(*) FROM things").Scan(&rows); err != nil {
			readErr = err
		}
	})
	writeDone := make(chan error, 1)
	go func() { writeDone <- w.Write(context.Background(), hub.Event{Topic: "held"}, insertThing) }()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- database.Close(dir) }()
	// A close stops the Writer from publishing and then waits for this change,
	// so waiting for that flag proves the close has reached its drain before the
	// change is let go.
	waitClosed(t, w)
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatalf("the publish read the database after its handle closed: %v", readErr)
	}
}

// waitClosed waits for a close to reach w's drain. It yields instead of
// sleeping, and gives up with a failure instead of hanging the suite when a
// close never drains, because draining is the change this file guards.
func waitClosed(t *testing.T, w *Writer) {
	t.Helper()
	for yields := 0; ; yields++ {
		w.events.Lock()
		closed := w.closed
		w.events.Unlock()
		if closed {
			return
		}
		if yields > 1<<20 {
			t.Fatal("the close never stopped this writer's publishing")
		}
		runtime.Gosched()
	}
}
