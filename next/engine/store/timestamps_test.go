package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFirstModelTimestamp(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0),('other','r','other','p','open','',0);
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',1,'','',0,0,0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, step := range []struct {
		session contract.SessionID
		model   int64
		want    int64
	}{{"other", 100, 0}, {"s", 0, 0}, {"s", 123, 123}, {"s", 456, 123}, {"s", 123, 123}, {"s", 0, 123}} {
		if step.model != 0 {
			if err := s.MarkTurnModelSent(ctx, step.session, "t", time.UnixMilli(step.model)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.MarkTurnActive(ctx, "s", "t", time.Now()); err != nil {
			t.Fatal(err)
		}
		var got int64
		if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='t'").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != step.want {
			t.Fatalf("model_sent_at=%d want %d", got, step.want)
		}
	}
}

func TestWarmModelStampDoesNotEnterWriter(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',1,'','',0,0,0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTurnModelSent(context.Background(), "s", "t", time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 3; i++ {
		if err := s.MarkTurnModelSent(ctx, "s", "t", time.Now()); err != nil {
			t.Fatalf("warm request entered store: %v", err)
		}
	}
}

func TestModelStampConcurrentRetryAndClear(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	ctx := context.Background()
	if _, err := s.writer.Exec(`CREATE TRIGGER fail_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(ABORT, 'stamp unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(100)); err == nil {
				t.Error("expected failed stamp")
			}
		}()
	}
	wg.Wait()
	if n := strings.Count(logs.String(), "model handoff: stamp turn turn:"); n != 1 {
		t.Fatalf("logged %d times: %s", n, logs.String())
	}
	if _, ok := s.stamped.Load(contract.TurnID("turn")); ok {
		t.Fatal("failed stamp cached")
	}
	if _, err := s.writer.Exec("DROP TRIGGER fail_stamp"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(int64(200+i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var at int64
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='turn'").Scan(&at); err != nil || at < 200 || at >= 220 {
		t.Fatalf("first stamp=%d err=%v", at, err)
	}
	// Concurrent first requests may each write. After success, no later one may.
	writes := 0
	s.SetWake(func([]contract.SessionID, []contract.Event) { writes++ })
	for i := 0; i < 20; i++ {
		if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(999)); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 {
		t.Fatalf("warm writer commits=%d", writes)
	}
	s.clearModelStamps()
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(999)); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("clear should cost one conditional update, got %d", writes)
	}
	var after int64
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='turn'").Scan(&after); err != nil || after != at {
		t.Fatalf("overwrote first stamp: before=%d after=%d err=%v", at, after, err)
	}
	endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	if _, err := s.writer.Exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('next','child',2000000,'','',0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTurnModelSent(ctx, "child", "next", time.UnixMilli(500)); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='next'").Scan(&after); err != nil || after != 500 {
		t.Fatalf("new stamp=%d err=%v", after, err)
	}
}

func TestModelStampClearBoundsLateTerminalKeys(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	if modelStampRetention != 10*time.Minute {
		t.Fatal("unexpected map clear interval")
	}
	if err := s.MarkTurnModelSent(context.Background(), "child", "turn", time.UnixMilli(123)); err != nil {
		t.Fatal(err)
	}
	endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	// Publication racing terminal deletion is explicitly allowed. Periodic clear
	// bounds both maps without any per-turn state, mutex or waiter coordination.
	for i := 0; i < 1000; i++ {
		key := contract.TurnID(fmt.Sprint("ended-", i))
		s.stamped.Store(key, true)
		s.logged.Store(key, true)
	}
	s.clearModelStamps()
	s.stamped.Range(func(_, _ any) bool { t.Error("stamped key survived clear"); return false })
	s.logged.Range(func(_, _ any) bool { t.Error("logged key survived clear"); return false })
	if err := s.MarkTurnModelSent(context.Background(), "child", "turn", time.UnixMilli(999)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.stamped.Load(contract.TurnID("turn")); ok {
		t.Fatal("ended turn incorrectly counted as already stamped")
	}
}

func TestForcedSessionClosePreservesStampHistoryAndBoundsKeys(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	ctx := context.Background()
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(123)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE sessions SET state='closing' WHERE id='child'; UPDATE boxes SET state='deleted' WHERE session='child'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "forced-close", Time: time.Unix(1001, 0), Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionClosed{Session: "child", Reason: "discard"}}
	if err := s.CloseSession(ctx, "child", event); err != nil {
		t.Fatal(err)
	}
	// A late in-flight publication is harmless and is bounded by the clear.
	s.stamped.Store(contract.TurnID("turn"), true)
	s.logged.Store(contract.TurnID("turn"), true)
	s.clearModelStamps()
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(999)); err != nil {
		t.Fatal(err)
	}
	var at int64
	var ended sql.NullInt64
	if err := s.Read().QueryRow("SELECT model_sent_at,ended_at FROM turns WHERE id='turn'").Scan(&at, &ended); err != nil || at != 123 || ended.Valid {
		t.Fatalf("close changed retained turn: stamp=%d ended=%v err=%v", at, ended, err)
	}
	s.stamped.Range(func(_, _ any) bool { t.Error("closed turn recached"); return false })
}

func TestZeroRowModelStampRequiresPersistedValue(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	if _, err := s.writer.Exec("CREATE TRIGGER ignore_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(123)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.stamped.Load(contract.TurnID("turn")); ok {
		t.Fatal("zero-row update cached without a persisted value")
	}
	if _, err := s.writer.Exec("DROP TRIGGER ignore_stamp"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(456)); err != nil {
		t.Fatal(err)
	}
	var at int64
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='turn'").Scan(&at); err != nil || at != 456 {
		t.Fatalf("at=%d err=%v", at, err)
	}
}

func TestStampAfterStoreCloseLogsOnce(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	for i := 0; i < 2; i++ {
		if err := s.MarkTurnModelSent(context.Background(), "s", "turn", time.Now()); err == nil {
			t.Fatal("closed store accepted stamp")
		}
	}
	if n := strings.Count(logs.String(), "model handoff: stamp turn turn:"); n != 1 {
		t.Fatalf("closed store logged %d times: %s", n, logs.String())
	}
}
