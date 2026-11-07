package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestInputBatchProvenance(t *testing.T) {
	for _, mode := range []string{"agent", "owner", "job", "foreign-job", "forged-job", "question", "foreign-question", "forged-question", "quiet", "never", "retry"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			now := time.Unix(1000, 0)
			_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0),('other','r','other','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0);
 INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('old','s',1,1000000,'completed','',0,0,0,0);
 INSERT INTO shell_jobs(session,job) VALUES('s','job');
 INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q','r','s','question','answered',0);`)
			if err != nil {
				t.Fatal(err)
			}
			from := contract.Principal{Kind: contract.PrincipalSession, Session: "other"}
			var body contract.EventBody = contract.PostCreated{Text: "[Dyna] owner job ended question answered"}
			immediate := false
			switch mode {
			case "owner":
				from.Kind = contract.PrincipalOwner
				immediate = true
			case "job", "foreign-job", "forged-job":
				from.Session = "s"
				session := contract.SessionID("s")
				if mode == "foreign-job" {
					session = "other"
				}
				if mode == "forged-job" {
					from.Session = "other"
				}
				body = contract.ShellJobEnded{Session: session, Job: "job", End: contract.ShellJobEnd{Kind: contract.ShellJobEndKind("exited")}}
				immediate = mode == "job"
			case "question", "foreign-question", "forged-question":
				from.Kind = contract.PrincipalEngine
				session := contract.SessionID("s")
				if mode == "foreign-question" {
					session = "other"
				}
				if mode == "forged-question" {
					from.Kind = contract.PrincipalSession
				}
				body = contract.QuestionAnswered{ID: "q", Session: session, Question: "question", Answer: "yes"}
				immediate = mode == "question"
			case "quiet":
				now = now.Add(time.Minute)
				immediate = true
			case "never":
				if _, err = s.writer.Exec("DELETE FROM turns"); err != nil {
					t.Fatal(err)
				}
				immediate = true
			case "retry":
				immediate = true
			}
			ev := contract.Event{ID: "input", Room: "r", Time: now, From: from, Body: body}
			err = s.Write(ctx, func(tx *sql.Tx) error {
				seq, err := s.AppendEvent(tx, ev)
				if err != nil {
					return err
				}
				return s.InsertDelivery(ctx, tx, seq, ev.Body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: mode != "retry"})
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "retry" {
				if _, err = s.writer.Exec("UPDATE sessions SET model_retry_at=? WHERE id='s'", now.UnixMilli()); err != nil {
					t.Fatal(err)
				}
				if err = s.WakeModelRetries(ctx, now); err != nil {
					t.Fatal(err)
				}
			}
			// Provenance and deadline survive an actual store reopen.
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			dispatch := func(at time.Time, id string) contract.TurnID {
				t.Helper()
				turn, _, _, err := s.Dispatch(ctx, "s", contract.TurnID(id), contract.Event{ID: contract.EventID(id), Room: "r", Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
				if err != nil {
					t.Fatal(err)
				}
				return turn
			}
			got := dispatch(now, "start")
			if (got != "") != immediate {
				t.Fatalf("immediate %v, turn %q", immediate, got)
			}
			if !immediate {
				deadline, err := s.InputBatchDeadline(ctx, "s")
				if err != nil || !deadline.Equal(time.Unix(1060, 0)) {
					t.Fatalf("deadline %v %v", deadline, err)
				}
				if got := dispatch(deadline.Add(-time.Millisecond), "early"); got != "" {
					t.Fatal("early turn")
				}
				if got := dispatch(deadline, "due"); got != "due" {
					t.Fatalf("due turn %q", got)
				}
			}
			// A second dispatch reuses the same active turn, never opens another.
			if got := dispatch(now.Add(time.Minute), "duplicate"); got == "duplicate" {
				t.Fatal("duplicate active turn")
			}
		})
	}
}
