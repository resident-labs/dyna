package engine

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the actual framed stream: held inputs must not leak TurnStart.
func TestInputBatchFramedDeadline(t *testing.T) {
	for _, mode := range []string{"deadline", "owner", "reconnect", "subscription", "peer-parent", "peer-sibling", "peer-ancestor", "peer-cross-root"} {
		t.Run(mode, func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			var clock atomic.Int64
			clock.Store(s.now().UnixMilli())
			s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
			ticks := make(chan time.Time, 1)
			armed := make(chan time.Duration, 4)
			s.batchTimer = func(d time.Duration) (<-chan time.Time, func()) { armed <- d; return ticks, func() {} }
			caller := boxCaller(worker)
			ch, problem := testChannel(t, s, ctx, caller)
			if problem != nil {
				t.Fatal(problem)
			}
			first := (<-ch).Body.(contract.TurnStart)
			if _, p := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnCompleted}); p != nil {
				t.Fatal(p)
			}
			post := func(id string) {
				t.Helper()
				e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.PostCreated{Text: id})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.store.Post(ctx, e, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
					t.Fatal(err)
				}
			}
			post("one")
			select {
			case d := <-armed:
				if d != time.Minute {
					t.Fatalf("deadline delay %v", d)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timer not armed")
			}
			select {
			case msg := <-ch:
				t.Fatalf("early frame: %T", msg.Body)
			case <-time.After(100 * time.Millisecond):
			}
			clock.Add(20_000)
			post("two")
			select {
			case msg := <-ch:
				t.Fatalf("arrival extended or leaked frame: %T", msg.Body)
			case <-time.After(100 * time.Millisecond):
			}
			select {
			case d := <-armed:
				t.Fatalf("arrival replaced timer: %v", d)
			default:
			}
			if mode == "reconnect" {
				var p *contract.Error
				ch, p = testChannel(t, s, ctx, caller)
				if p != nil {
					t.Fatal(p)
				}
				select {
				case d := <-armed:
					if d != 40*time.Second {
						t.Fatalf("reconnect reset deadline: %v", d)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("reconnect has no deadline")
				}
			}
			if strings.HasPrefix(mode, "peer-") {
				workerSession, err := s.store.Session(ctx, worker)
				if err != nil {
					t.Fatal(err)
				}
				donor := workerSession.Parent
				err = s.store.Write(ctx, func(tx *sql.Tx) error {
					if mode == "peer-sibling" {
						donor = "peer"
						_, err = tx.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('peer',?,'peer','test','open','',0,?)", room, workerSession.Parent)
					} else if mode == "peer-ancestor" {
						_, err = tx.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('middle',?,'middle','test','open','',0,?)", room, workerSession.Parent)
						if err == nil {
							_, err = tx.ExecContext(ctx, "UPDATE sessions SET parent='middle' WHERE id=?", worker)
						}
					} else if mode == "peer-cross-root" {
						donor = "peer"
						_, err = tx.ExecContext(ctx, "INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('other-room','other',0,0,0); INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('peer','other-room','root','test','open','',0)")
						if err == nil {
							_, err = tx.ExecContext(ctx, "UPDATE sessions SET parent=NULL WHERE id=?", worker)
						}
					}
					if err != nil {
						return err
					}
					e, err := s.event(room, contract.Principal{Kind: contract.PrincipalSession, Session: donor}, contract.PostCreated{Text: "directing work"})
					if err != nil {
						return err
					}
					seq, err := s.store.AppendEvent(tx, e)
					if err != nil {
						return err
					}
					return s.store.InsertDelivery(ctx, tx, seq, e.Body, contract.Delivery{To: caller, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
				})
				if err != nil {
					t.Fatal(err)
				}
			} else if mode == "owner" {
				if _, p := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "now"}); p != nil {
					t.Fatal(p)
				}
			} else if mode == "subscription" {
				// Process a post subscription only after the ordinary delivery has
				// armed its wait: promotion must kick the same stream immediately.
				if _, p := s.SubscriptionAdd(ctx, caller, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: string(worker), On: []contract.EventType{"post.created"}, Wake: string(worker)}); p != nil {
					t.Fatal(p)
				}
				if _, err := s.store.DispatchEvents(ctx, 256); err != nil {
					t.Fatal(err)
				}
			} else {
				clock.Add(40_000)
				// Fire the scheduler, not another input or a manual dispatch.
				ticks <- s.now()
			}
			select {
			case msg := <-ch:
				start, ok := msg.Body.(contract.TurnStart)
				want := 2
				if mode == "owner" || strings.HasPrefix(mode, "peer-") {
					want = 3
				}
				if !ok || len(start.Inputs) != want {
					t.Fatalf("deadline frame: %+v", msg.Body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no deadline turn")
			}
		})
	}
}
