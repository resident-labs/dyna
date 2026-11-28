package model

import (
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestQuotaReadReleasesRetriesOnlyOnRecovery(t *testing.T) {
	const healthy = `"status_code":200,"windows":[{"kind":"weekly_all","utilization":50},{"kind":"weekly_scoped","scope":"Fable","utilization":0}]`
	for _, tc := range []struct {
		name    string
		reply   string
		gap     string
		release bool
	}{
		{name: "recovered", reply: healthy, release: true},
		{name: "recovered after unavailable", reply: healthy, gap: `"status_code":500`, release: true},
		{name: "recovered after no windows", reply: healthy, gap: `"status_code":200`, release: true},
		{name: "still spent", reply: `"status_code":200,"windows":[{"kind":"weekly_all","utilization":50},{"kind":"weekly_scoped","scope":"Fable","utilization":102}]`},
		{name: "unavailable", reply: `"status_code":500`},
		{name: "no windows", reply: `"status_code":200`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := quotaStore(t)
			now := time.Unix(1000, 0)
			if err := st.CreateRoom(ctx, "r", "r", now); err != nil {
				t.Fatal(err)
			}
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES
('pig','r','pig','fable','open','',0),('agy','r','agy','neon','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,harness) VALUES
('box-pig','pig','','agent','ready','','',0,0,'pig'),('box-agy','agy','','agent','ready','','',0,0,'agy')`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			owner := contract.Principal{Kind: contract.PrincipalOwner}
			engine := contract.Principal{Kind: contract.PrincipalEngine}
			deadline := make(map[contract.SessionID]int64)
			readDeadline := func(id contract.SessionID) (at int64) {
				t.Helper()
				if err := st.Read().QueryRowContext(ctx, "SELECT model_retry_at FROM sessions WHERE id=?", id).Scan(&at); err != nil {
					t.Fatal(err)
				}
				return at
			}
			for _, id := range []contract.SessionID{"pig", "agy"} {
				session := contract.Principal{Kind: contract.PrincipalSession, Session: id}
				post := contract.Event{ID: "input-" + contract.EventID(id), Time: now, Room: "r", From: owner, Body: contract.PostCreated{Text: "work"}}
				if _, err := st.Post(ctx, post, []contract.Delivery{{To: session, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
					t.Fatal(err)
				}
				turn := contract.TurnID(id)
				start := contract.Event{ID: "start-" + contract.EventID(id), Time: now, Room: "r", From: engine, Body: contract.TurnStarted{}}
				if active, _, _, err := st.Dispatch(ctx, id, turn, start, false); err != nil || active != turn {
					t.Fatalf("initial dispatch for %s = %q: %v", id, active, err)
				}
				failure := contract.Event{ID: "end-" + contract.EventID(id), Time: now, Room: "r", From: session, Body: contract.TurnEnded{Session: id, Turn: turn, Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed}}
				if err := st.EndTurn(ctx, id, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: 82700, OneWake: id == "agy"}, failure); err != nil {
					t.Fatal(err)
				}
				deadline[id] = readDeadline(id)
				if deadline[id] <= now.Add(quotaAge).UnixMilli() {
					t.Fatalf("expected a future retry for %s, got %d", id, deadline[id])
				}
			}
			reply := `"status_code":200,"windows":[{"kind":"weekly_all","utilization":50},{"kind":"weekly_scoped","scope":"Fable","utilization":100}]`
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/dyna/credentials":
					fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"invented","provider":"claude","state":"unknown"}]}`)
				case "/v0/management/auth-files/quota":
					fmt.Fprintf(w, `{"files":[{"auth_index":"invented",%s}]}`, reply)
				case "/v8/management/routing/status":
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected request: %s", r.URL)
				}
			})
			q := quotaProxy("http://fixture", quotaHandlerTransport{handler}).Quotas("key", st, func() time.Time { return now })
			var releases int
			q.OnRecovery(func(at time.Time) {
				releases++
				if err := st.ReleaseModelRetries(ctx, at); err != nil {
					t.Fatal(err)
				}
			})
			if _, err := q.Read(ctx); err != nil || releases != 0 {
				t.Fatalf("first read: releases = %d: %v", releases, err)
			}
			if tc.gap != "" {
				now = now.Add(quotaAge)
				reply = tc.gap
				if _, err := q.Read(ctx); err != nil || releases != 0 {
					t.Fatalf("inconclusive read: releases = %d: %v", releases, err)
				}
			}
			now = now.Add(quotaAge)
			reply = tc.reply
			for range 2 { // a second whole read of the new value must not release again
				if _, err := q.Read(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if (releases == 1) != tc.release || releases > 1 {
				t.Fatalf("recovery hook ran %d times", releases)
			}
			if tc.release {
				deadline["pig"] = now.UnixMilli()
			}
			for id, want := range deadline {
				if got := readDeadline(id); got != want {
					t.Errorf("%s retry = %d, want %d", id, got, want)
				}
			}
			startRetry := func() contract.TurnID {
				t.Helper()
				start := contract.Event{ID: "retry-start", Time: now, Room: "r", From: engine, Body: contract.TurnStarted{}}
				turn, inputs, _, err := st.Dispatch(ctx, "pig", "retry", start, false)
				if err != nil || (turn == "retry" && len(inputs) != 1) {
					t.Fatalf("retry dispatch: %s with %d inputs: %v", turn, len(inputs), err)
				}
				return turn
			}
			if got := startRetry(); got != "" {
				t.Fatalf("quota read woke a retry before the sweep: %s", got)
			}
			if err := st.WakeModelRetries(ctx, now); err != nil {
				t.Fatal(err)
			}
			if turn := startRetry(); (turn == "retry") != tc.release {
				t.Fatalf("turn after sweep = %q, want retry: %t", turn, tc.release)
			}
		})
	}
}

func TestQuotaRecoveryIgnoresOlderCachedReadingThatFinishesLater(t *testing.T) {
	ctx := t.Context()
	st := quotaStore(t)
	var now, used atomic.Int64
	var routingCalls, releases atomic.Int32
	now.Store(1000)
	used.Store(50)
	waiting, proceed := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(proceed) })
	defer resume()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dyna/credentials":
			fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"invented","provider":"claude","state":"unknown"}]}`)
		case "/v0/management/auth-files/quota":
			fmt.Fprintf(w, `{"files":[{"auth_index":"invented","status_code":200,"windows":[{"kind":"weekly_all","utilization":%d}]}]}`, used.Load())
		case "/v8/management/routing/status":
			if routingCalls.Add(1) == 2 {
				close(waiting)
				<-proceed
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
	})
	q := quotaProxy("http://fixture", quotaHandlerTransport{handler}).Quotas("key", st, func() time.Time { return time.Unix(now.Load(), 0) })
	q.OnRecovery(func(time.Time) { releases.Add(1) })
	if _, err := q.Read(ctx); err != nil {
		t.Fatal(err)
	}
	// The second read takes the last healthy cached sample, then stalls in routing.
	now.Add(int64(quotaAge/time.Second) - 1)
	old := make(chan error, 1)
	go func() { _, err := q.Read(ctx); old <- err }()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("cached read did not reach routing")
	}
	// A new read refreshes the expired sample and returns spent before the old read finishes.
	used.Store(100)
	now.Add(1)
	if _, err := q.Read(ctx); err != nil {
		t.Fatal(err)
	}
	resume()
	if err := <-old; err != nil {
		t.Fatal(err)
	}
	if got := releases.Load(); got != 0 {
		t.Fatalf("older healthy read released retries %d times", got)
	}
	// The stale snapshot must not erase the spent observation for the real recovery either.
	used.Store(50)
	now.Add(int64(quotaAge / time.Second))
	if _, err := q.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("fresh recovery released retries %d times, want 1", got)
	}
}
