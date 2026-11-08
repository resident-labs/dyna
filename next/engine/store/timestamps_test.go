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

func TestModelStampRetryConcurrentAndTurnCleanup(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	ctx := context.Background()
	// SQL aborts are transaction-local failures: the writer stays usable.
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
	if n := strings.Count(logs.String(), "stamp unavailable"); n != 1 {
		t.Fatalf("logged %d times: %s", n, logs.String())
	}
	if s.modelSent["turn"].stamped {
		t.Fatal("failed stamp cached as success")
	}
	if _, err := s.writer.Exec(`DROP TRIGGER fail_stamp; CREATE TABLE stamp_writes(n INTEGER); INSERT INTO stamp_writes VALUES(0); CREATE TRIGGER count_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN UPDATE stamp_writes SET n=n+1; END`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(200)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var writes int
	if err := s.Read().QueryRow("SELECT n FROM stamp_writes").Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("writes=%d err=%v", writes, err)
	}
	var at int64
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='turn'").Scan(&at); err != nil || at != 200 {
		t.Fatalf("at=%d err=%v", at, err)
	}
	// Losing in-memory state (as on restart) must also preserve the first stamp
	// without a redundant writer transaction.
	s.modelSent = nil
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(250)); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT n FROM stamp_writes").Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("persisted stamp entered writer: writes=%d err=%v", writes, err)
	}
	// End races with delayed requests; none may recreate an ended turn entry.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(300)) }()
	}
	endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted})
	wg.Wait()
	if len(s.modelSent) != 0 {
		t.Fatalf("retained ended turns: %v", s.modelSent)
	}
	if _, err := s.writer.Exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('next','child',2000000,'','',0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	// Captured old identity must not target the new active turn or leak memory.
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(400)); err != nil {
		t.Fatal(err)
	}
	if len(s.modelSent) != 0 {
		t.Fatal("old request recreated entry")
	}
	if err := s.MarkTurnModelSent(ctx, "parent", "next", time.UnixMilli(400)); err != nil {
		t.Fatal(err)
	}
	if len(s.modelSent) != 0 {
		t.Fatal("wrong session created entry")
	}
	if _, err := s.writer.Exec(`CREATE TRIGGER fail_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(ABORT, 'stamp unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.MarkTurnModelSent(ctx, "child", "next", time.UnixMilli(450)); err == nil {
			t.Fatal("expected new turn stamp failure")
		}
	}
	if n := strings.Count(logs.String(), "stamp unavailable"); n != 2 {
		t.Fatalf("new turn did not get independent once-only logging: %s", logs.String())
	}
	if _, err := s.writer.Exec("DROP TRIGGER fail_stamp"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTurnModelSent(ctx, "child", "next", time.UnixMilli(500)); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='next'").Scan(&at); err != nil || at != 500 {
		t.Fatalf("new at=%d err=%v", at, err)
	}
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='turn'").Scan(&at); err != nil || at != 200 {
		t.Fatalf("old at=%d err=%v", at, err)
	}
}

func TestForcedSessionCloseCleansModelStamp(t *testing.T) {
	for _, stampFails := range []bool{false, true} {
		t.Run(fmt.Sprint("stampFails=", stampFails), func(t *testing.T) {
			s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
			ctx := context.Background()
			if stampFails {
				if _, err := s.writer.Exec(`CREATE TRIGGER fail_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(ABORT, 'stamp unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(123))
			if (err != nil) != stampFails {
				t.Fatalf("stamp error=%v", err)
			}
			// Match lifecycle.closePass: a disconnected/discarded box reaches deleted
			// without TurnOutput/EndTurn, then CloseSession commits the forced close.
			if err := s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE sessions SET state='closing' WHERE id='child'; UPDATE boxes SET state='deleted' WHERE session='child'")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			event := contract.Event{ID: "forced-close", Time: time.Unix(1001, 0), Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionClosed{Session: "child", Reason: "discard"}}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := s.CloseSession(canceled, "child", event); err == nil {
				t.Fatal("expected canceled close")
			}
			if len(s.modelSent) != 1 {
				t.Fatal("failed close removed live stamp state")
			}
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _ = s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(124)) }()
			}
			if err := s.CloseSession(ctx, "child", event); err != nil {
				t.Fatal(err)
			}
			wg.Wait()
			if len(s.modelSent) != 0 {
				t.Fatalf("forced close retained turn memory: %+v", s.modelSent)
			}
			for i := 0; i < 3; i++ {
				if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(999)); err != nil {
					t.Fatal(err)
				}
			}
			if len(s.modelSent) != 0 {
				t.Fatal("delayed request recreated closed session state")
			}
			var at int64
			var ended sql.NullInt64
			if err := s.Read().QueryRow("SELECT model_sent_at,ended_at FROM turns WHERE id='turn'").Scan(&at, &ended); err != nil {
				t.Fatal(err)
			}
			want := int64(123)
			if stampFails {
				want = 0
			}
			if at != want || ended.Valid {
				t.Fatalf("close changed turn history: stamp=%d ended=%v", at, ended)
			}
		})
	}
}

func TestZeroRowModelStampIsNotCached(t *testing.T) {
	s, _ := endTurnFixture(t, []contract.ReplyChannel{{Kind: contract.ReplyNone}}, contract.PostCreated{Text: "done", Home: endTurnFeed})
	if _, err := s.writer.Exec("CREATE TRIGGER ignore_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.MarkTurnModelSent(ctx, "child", "turn", time.UnixMilli(123)); err != nil {
		t.Fatal(err)
	}
	if s.modelSent["turn"].stamped {
		t.Fatal("zero-row update cached as persisted stamp")
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
