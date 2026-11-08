package store

import (
	"bytes"
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"log"
	"strings"
	"testing"
	"time"
)

// Preserve the reviewer's observable cancellation/waiter/retry scenario without
// relying on the pointer states which the two-sync.Map replacement removes.
func TestCanceledStampLookupKeepsWaitersCoordinated(t *testing.T) { stampCancellation(t, false) }
func TestCanceledStampLookupLogsWriteFailureOnce(t *testing.T)    { stampCancellation(t, true) }
func stampCancellation(t *testing.T, fail bool) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	ctx := context.Background()
	if fail {
		if _, err := s.writer.Exec("CREATE TRIGGER fail_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(ABORT, 'stamp unavailable'); END"); err != nil {
			t.Fatal(err)
		}
	}
	// The old implementation blocked its first SELECT here. The replacement
	// never acquires a read connection, so cancellation is at writer admission.
	s.Read().SetMaxOpenConns(1)
	conn, err := s.Read().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	entered, release, gate := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { gate <- s.Write(ctx, func(*sql.Tx) error { close(entered); <-release; return nil }) }()
	<-entered
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- s.MarkTurnModelSent(canceled, "child", "turn", time.UnixMilli(100)) }()
	deadline := time.Now().Add(2 * time.Second)
	for s.mu.TryLock() {
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("first attempt did not reach writer")
		}
		time.Sleep(time.Millisecond)
	}
	waiter := make(chan error, 1)
	go func() { waiter <- s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(200)) }()
	cancel()
	if err := <-first; err == nil {
		t.Fatal("canceled engine-context attempt succeeded")
	}
	conn.Close()
	deadline = time.Now().Add(2 * time.Second)
	for s.mu.TryLock() {
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("waiter did not reach writer")
		}
		time.Sleep(time.Millisecond)
	}
	replacement := make(chan error, 1)
	go func() { replacement <- s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(300)) }()
	close(release)
	released = true
	if err := <-gate; err != nil {
		t.Fatal(err)
	}
	e1, e2 := <-waiter, <-replacement
	if fail {
		if e1 == nil || e2 == nil {
			t.Fatalf("expected store errors: %v %v", e1, e2)
		}
	} else {
		if e1 != nil || e2 != nil {
			t.Fatalf("stamp errors: %v %v", e1, e2)
		}
		var at int64
		if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='turn'").Scan(&at); err != nil || at < 200 || at > 300 {
			t.Fatalf("persisted=%d err=%v", at, err)
		}
		writes := 0
		s.SetWake(func([]contract.SessionID, []contract.Event) { writes++ })
		if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(400)); err != nil {
			t.Fatal(err)
		}
		if writes != 0 {
			t.Fatalf("redundant warm writer commits=%d", writes)
		}
	}
	if n := strings.Count(logs.String(), "model handoff: stamp turn turn:"); n != 1 {
		t.Fatalf("one active turn logged %d failures: %s", n, logs.String())
	}
}
