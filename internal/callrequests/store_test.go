package callrequests

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{Root: t.TempDir()}
	t.Cleanup(func() {
		if err := database.Close(s.Root); err != nil {
			t.Error(err)
		}
	})
	return s
}

func ask(t *testing.T, s *Store, room string) Request {
	t.Helper()
	r, created, err := s.Ask(room, "Please join", "session")
	if err != nil || !created {
		t.Fatalf("Ask: created=%v err=%v", created, err)
	}
	return r
}

func TestRepeatedAskAndRoomIDs(t *testing.T) {
	s := testStore(t)
	first := ask(t, s, "one")
	r, created, err := s.Ask("one", "New reason", "other")
	if err != nil || created || r.ID != first.ID || r.Reason != "New reason" || r.Asker != first.Asker || !r.AskedAt.Equal(first.AskedAt) {
		t.Fatalf("repeated Ask: %+v %v %v", r, created, err)
	}
	if r := ask(t, s, "two"); r.ID != "c1" {
		t.Fatalf("other room ID: %s", r.ID)
	}
	if _, changed, err := s.Close("one", first.ID, "joined", "owner"); err != nil || !changed {
		t.Fatalf("Close: %v %v", changed, err)
	}
	if r := ask(t, s, "one"); r.ID != "c2" {
		t.Fatalf("next ID: %s", r.ID)
	}
	rows, err := s.List()
	if err != nil || len(rows) != 3 {
		t.Fatalf("all history: %+v %v", rows, err)
	}
	rows, err = s.List("one")
	if err != nil || len(rows) != 2 {
		t.Fatalf("room history: %+v %v", rows, err)
	}
	if _, err := s.Open("absent"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Get("two", "c2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get crossed rooms: %v", err)
	}
}

func TestConcurrentAskUnique(t *testing.T) {
	s := testStore(t)
	const count = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan bool, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, created, err := (&Store{Root: s.Root}).Ask("room", "Reason", "session")
			if err != nil || r.ID != "c1" {
				t.Errorf("Ask: %+v %v", r, err)
			}
			results <- created
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for value := range results {
		if value {
			created++
		}
	}
	rows, err := s.List("room")
	if created != 1 || err != nil || len(rows) != 1 {
		t.Fatalf("created=%d rows=%+v err=%v", created, rows, err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO call_requests(room,id,reason,asker,state,asked_at) VALUES('room','c99','reason','asker','ringing',0)`)
	if err == nil {
		t.Fatal("SQLite allowed a second ringing request")
	}
}

func TestCloseRace(t *testing.T) {
	s := testStore(t)
	r := ask(t, s, "room")
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan bool, 3)
	for _, state := range []string{"joined", "declined", "withdrawn"} {
		wg.Add(1)
		go func(state string) {
			defer wg.Done()
			<-start
			closed, changed, err := s.Close(r.Room, r.ID, state, state)
			if err != nil || closed.State == "ringing" || closed.ClosedAt == nil || closed.ClosedBy != closed.State {
				t.Errorf("Close: %+v %v", closed, err)
			}
			results <- changed
		}(state)
	}
	close(start)
	wg.Wait()
	close(results)
	changes := 0
	for changed := range results {
		if changed {
			changes++
		}
	}
	if changes != 1 {
		t.Fatalf("close winners: %d", changes)
	}
	if _, err := s.Open(r.Room); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Open after Close: %v", err)
	}
	if _, changed, err := s.Close(r.Room, r.ID, "ringing", "owner"); err == nil || changed {
		t.Fatalf("invalid close: %v %v", changed, err)
	}
	closed, err := s.Shown(r.Room, r.ID)
	if err != nil || closed.ShownAt != nil {
		t.Fatalf("shown closed request: %+v %v", closed, err)
	}
	if _, claimed, err := s.ClaimPush(r.Room, r.ID); err != nil || claimed {
		t.Fatalf("push closed request: %v %v", claimed, err)
	}
}

func TestDeliveryClaims(t *testing.T) {
	s := testStore(t)
	r := ask(t, s, "room")
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan bool, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row, claimed, err := s.ClaimPush(r.Room, r.ID)
			if err != nil || row.PushSentAt == nil {
				t.Errorf("ClaimPush: %+v %v", row, err)
			}
			results <- claimed
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("push claims: %d", claims)
	}
	shown, err := s.Shown(r.Room, r.ID)
	if err != nil || shown.ShownAt == nil {
		t.Fatalf("Shown: %+v %v", shown, err)
	}
	again, err := s.Shown(r.Room, r.ID)
	if err != nil || !again.ShownAt.Equal(*shown.ShownAt) {
		t.Fatalf("Shown changed timestamp: %+v %v", again, err)
	}
	other := ask(t, s, "other")
	if _, err := s.Shown(other.Room, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.ClaimPush(other.Room, other.ID); err != nil || claimed {
		t.Fatalf("push shown request: %v %v", claimed, err)
	}
}

func TestPersistenceAndCloseDelivery(t *testing.T) {
	s := testStore(t)
	r := ask(t, s, "room")
	if err := s.MarkCloseDelivered(r.Room, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.Get(r.Room, r.ID)
	if err != nil || current.CloseDelivered {
		t.Fatalf("delivered ringing request: %+v %v", current, err)
	}
	if _, _, err := s.ClaimPush(r.Room, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Close(r.Room, r.ID, "withdrawn", "session"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.Root); err != nil {
		t.Fatal(err)
	}
	reopened := &Store{Root: s.Root}
	current, err = reopened.Get(r.Room, r.ID)
	if err != nil || current.State != "withdrawn" || current.CloseDelivered || current.PushSentAt == nil || current.ClosedAt == nil {
		t.Fatalf("restart: %+v %v", current, err)
	}
	if err := reopened.MarkCloseDelivered(r.Room, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.MarkCloseDelivered(r.Room, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.Root); err != nil {
		t.Fatal(err)
	}
	current, err = reopened.Get(r.Room, r.ID)
	if err != nil || !current.CloseDelivered {
		t.Fatalf("close delivery restart: %+v %v", current, err)
	}
	if r := ask(t, reopened, "room"); r.ID != "c2" {
		t.Fatalf("restart reused ID: %s", r.ID)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "dyna.db")); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Ext(file.Name()) == ".json" {
			t.Fatalf("JSON state file: %s", file.Name())
		}
	}
}

func TestWritesUseSharedWriterWithoutDuplicateEvents(t *testing.T) {
	s := testStore(t)
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	var events []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) {
		events = append(events, ev)
	})
	r := ask(t, s, "room")
	if _, _, err := s.Ask(r.Room, "Updated reason", "session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Close(r.Room, r.ID, "withdrawn", "session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Close(r.Room, "missing", "withdrawn", "session"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing request: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("published %v; the room watch already publishes call requests", events)
	}
	if writes := state.For(db).Health().Writes; writes != 4 {
		t.Fatalf("shared writer recorded %d writes; want four attempts", writes)
	}
	row, err := s.Get(r.Room, r.ID)
	if err != nil || row.State != "withdrawn" {
		t.Fatalf("committed close: %+v, %v", row, err)
	}
}
