package activity

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestTurnPublisherHostedFramesAndRetention(t *testing.T) {
	dir := t.TempDir()
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_records(id TEXT PRIMARY KEY); CREATE TABLE runs(id TEXT PRIMARY KEY); INSERT INTO session_records(id) VALUES('ses_1')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	key := Key{Kind: Turn, Owner: "ses_1", Number: 1}
	p := NewPublisher(dir, key, Record{Signal: Signal{CPU: CPUUnsupported}, Lifecycle: CopierRunning, Reason: ReasonOutputUnobserved})
	p.Coverage(false)
	p.Output()
	p.End()
	<-p.Done()
	now := time.Now()
	got, err := Read(db, key, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != CopierEnded || got.Signal.Copier != CopierUnknown || got.Reason != ReasonPartialFrameUnobserved || got.Signal.LastOutput.IsZero() {
		t.Fatalf("activity = %+v", got)
	}
	expired, err := Read(db, key, got.Signal.LeaseUntil)
	if err != nil || expired.Lifecycle != CopierUnknown || expired.Signal.CPU != CPUUnknown {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteOwner(context.Background(), conn, Turn, "ses_1"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := db.Exec(`DELETE FROM session_records WHERE id='ses_1'`); err != nil {
		t.Fatal(err)
	}
	if err := Put(context.Background(), db, key, got); err != nil {
		t.Fatal(err)
	}
	got, err = Read(db, key, now)
	if err != nil || got.Lifecycle != CopierUnknown {
		t.Fatalf("deleted activity = %+v, %v", got, err)
	}
}

func TestFinalPublicationDeadline(t *testing.T) {
	blocked := make(chan struct{})
	deadline := make(chan time.Time, 1)
	deadline <- time.Unix(1, 0)
	if Wait([]<-chan struct{}{blocked}, deadline) {
		t.Fatal("blocked publication succeeded")
	}
}
