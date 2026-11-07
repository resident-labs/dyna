package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
	"time"
)

func TestInputBatchProvenance(t *testing.T) {
	for _, mode := range []string{"agent", "owner", "job", "foreign-job", "forged-job", "question", "foreign-question", "forged-question", "quiet", "never", "retry", "subscription-event", "subscription-scheduled", "subscription-command", "sibling", "parent", "ancestor", "cross-root", "below", "cross-nonroot", "legacy-owner", "legacy-agent"} {
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
			switch mode {
			case "sibling":
				_, err = s.writer.Exec("INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('p','r','p','p','open','',0); UPDATE sessions SET parent='p' WHERE id IN ('s','other')")
			case "parent":
				_, err = s.writer.Exec("UPDATE sessions SET parent='other' WHERE id='s'")
			case "ancestor":
				_, err = s.writer.Exec("INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('p','r','p','p','open','',0,'other'); UPDATE sessions SET parent='p' WHERE id='s'")
			case "below":
				_, err = s.writer.Exec("UPDATE sessions SET parent='s' WHERE id='other'")
			case "cross-root", "cross-nonroot":
				_, err = s.writer.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r2','r2',0,0,0); UPDATE sessions SET room='r2' WHERE id='other'")
				if mode == "cross-nonroot" {
					_, err = s.writer.Exec("INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('p','r2','p','p','open','',0); UPDATE sessions SET parent='p' WHERE id='other'")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			from := contract.Principal{Kind: contract.PrincipalSession, Session: "other"}
			var body contract.EventBody = contract.PostCreated{Text: "[Dyna] owner job ended question answered"}
			immediate := false
			switch mode {
			case "legacy-agent":
				immediate = true
			case "legacy-owner":
				from.Kind = contract.PrincipalOwner
				immediate = true
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
			case "retry", "subscription-event", "subscription-scheduled", "sibling", "parent", "ancestor", "cross-root":
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
			if strings.HasPrefix(mode, "subscription-") {
				sub := Subscription{Subscription: contract.Subscription{ID: "sub", Scope: contract.ScopeSession, Session: "s", Wake: "s"}, Period: time.Minute, DueAt: now}
				if _, err = s.writer.Exec("INSERT INTO subscriptions(id,scope,session,every_ms,wake_kind,wake_id,run_image,run_size,created_at,due_at) VALUES('sub','session','s',60000,'session','s','','',0,?)", now.UnixMilli()); err != nil {
					t.Fatal(err)
				}
				if mode == "subscription-scheduled" {
					e := contract.Event{ID: "scheduled", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.HookDue{Subscription: "sub"}}
					if err = s.FireScheduledWake(ctx, sub, e); err != nil {
						t.Fatal(err)
					}
				} else {
					if mode == "subscription-command" {
						sub.Wake = ""
					}
					if err = s.Write(ctx, func(tx *sql.Tx) error {
						if err := tx.QueryRowContext(ctx, "SELECT seq FROM events WHERE id='input'").Scan(&ev.Seq); err != nil {
							return err
						}
						return s.fire(ctx, tx, sub, ev)
					}); err != nil {
						t.Fatal(err)
					}
				}
				// Removing the hook must not erase how its delivery was chosen.
				if _, err = s.writer.Exec("DELETE FROM subscription_queue; DELETE FROM subscriptions"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "retry" {
				if _, err = s.writer.Exec("UPDATE sessions SET model_retry_at=? WHERE id='s'", now.UnixMilli()); err != nil {
					t.Fatal(err)
				}
				if err = s.WakeModelRetries(ctx, now); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(mode, "legacy-") {
				if _, err = s.writer.Exec("ALTER TABLE deliveries DROP COLUMN immediate; UPDATE meta SET value='99' WHERE key='schema_version'"); err != nil {
					t.Fatal(err)
				}
			}
			// Provenance and deadline survive an actual store reopen.
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "legacy-") {
				var defaultValue string
				if err := s.Read().QueryRowContext(ctx, "SELECT dflt_value FROM pragma_table_info('deliveries') WHERE name='immediate'").Scan(&defaultValue); err != nil || defaultValue != "0" {
					t.Fatalf("new delivery default %q: %v", defaultValue, err)
				}
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
