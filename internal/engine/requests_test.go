package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/websvc"
)

func TestCachedRunnerReplyRequiresItsVerifiedSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller websvc.Caller
		allow  bool
	}{
		{"own session", websvc.Caller{Kind: websvc.Session, Session: "ses_alice"}, true},
		{"another session", websvc.Caller{Kind: websvc.Session, Session: "ses_bob"}, false},
		{"empty session", websvc.Caller{Kind: websvc.Session}, false},
		{"owner", websvc.Caller{Kind: websvc.Owner, Session: "ses_alice"}, false},
		{"worker", websvc.Caller{Kind: websvc.Worker, Session: "ses_alice"}, false},
		{"unverified", websvc.Caller{Kind: websvc.Unverified, Session: "ses_alice"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w, dir := thingsStore(t)
			const rid = "ses_alice/1/2/3/4"
			fingerprint, err := requestFingerprint("RunnerTurnEnd", nil)
			if err != nil {
				t.Fatal(err)
			}
			request := state.ForRequestCall(t.Context(), runnerRequestKey(rid), "RunnerTurnEnd", fingerprint)
			if err := w.FinishRequest(request, runnerRequestKey(rid), "RunnerTurnEnd", []byte(`"private input"`)); err != nil {
				t.Fatal(err)
			}
			called := false
			calls := map[string]websvc.Call{"RunnerTurnEnd": func(context.Context, []json.RawMessage) (any, error) {
				called = true
				return nil, errors.New("cached request reached its method")
			}}
			applyOnce(calls, w, t.Logf)
			enginetest.ServeCallers(t, dir, func(net.Conn) (websvc.Caller, error) { return tc.caller, nil }, calls)
			var result string
			err = engineclient.New("").Call(callspec.WithRequest(t.Context(), rid), "RunnerTurnEnd", nil, &result)
			if tc.allow {
				if err != nil || result != "private input" {
					t.Fatalf("own cached reply = %q, %v", result, err)
				}
			} else if err == nil || result != "" {
				t.Fatalf("caller %v received cached reply %q, error %v", tc.caller, result, err)
			}
			if called {
				t.Fatal("cached request reached its method")
			}
			// Remote contexts have no verified local caller. They must not
			// reach a cached runner result, even before the Local binding.
			if _, err := calls["RunnerTurnEnd"](callspec.WithRequest(t.Context(), rid), nil); err == nil {
				t.Fatal("context without a local caller received a cached runner reply")
			}
			if called {
				t.Fatal("context without a local caller reached the runner method")
			}
		})
	}
}

// thingsStore is a dyna.db with the request table and one table to write.
func thingsStore(t *testing.T) (*sql.DB, *state.Writer, string) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	if err := state.MigrateRequests(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE things(name TEXT)"); err != nil {
		t.Fatal(err)
	}
	return db, state.For(db), dir
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM things").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A request ID belongs to its caller: two paired devices, or two sessions,
// that send the same ID both apply, and neither gets the other's result. A
// caller's own retry still applies once.
func TestSameRequestIDFromTwoCallersAppliesTwice(t *testing.T) {
	db, w, _ := thingsStore(t)
	add := func(ctx context.Context, _ []json.RawMessage) (any, error) {
		return nil, w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES('a')")
			return err
		})
	}
	once := &requestRunner{w: w, logf: t.Logf}
	local := websvc.Principal{Kind: "local"}
	keys := []string{
		callerRequestKey(websvc.Caller{}, false, websvc.Principal{Kind: "device", ID: "dev_phone"}, "r1"),
		callerRequestKey(websvc.Caller{}, false, websvc.Principal{Kind: "device", ID: "dev_laptop"}, "r1"),
		callerRequestKey(websvc.Caller{Kind: websvc.Session, Session: "ses_alice"}, true, local, "r1"),
		callerRequestKey(websvc.Caller{Kind: websvc.Session, Session: "ses_bob"}, true, local, "r1"),
	}
	for i, key := range keys {
		if _, err := once.run(t.Context(), key, "AddThing", add, nil); err != nil {
			t.Fatal(err)
		}
		if got := countRows(t, db); got != i+1 {
			t.Fatalf("after caller %d (%s): %d rows, want %d", i, key, got, i+1)
		}
	}
	if _, err := once.run(t.Context(), keys[0], "AddThing", add, nil); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db); got != len(keys) {
		t.Fatalf("the first device's retry applied again: %d rows", got)
	}
}

// The same request twice gives one write and one event (requirement 9): the
// request ID goes from engineclient over the socket to the engine's call
// table, and the retry gets the first result.
func TestRequestRetryAppliesOnce(t *testing.T) {
	db, w, dataDir := thingsStore(t)
	var mu sync.Mutex
	events := 0
	w.SetPublish(func(hub.Event) { mu.Lock(); events++; mu.Unlock() })
	writes := 0
	calls, err := websvc.Bind([]websvc.Binding{{Name: "AddThing", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, name string) (int, error) {
			writes++
			err := w.Write(ctx, hub.Event{Topic: "things"}, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES(?)", name)
				return err
			})
			return writes, err
		}}})
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, w, t.Logf)
	enginetest.Serve(t, dataDir, calls)
	client := engineclient.New("")
	call := func(ctx context.Context) int {
		t.Helper()
		var got int
		if err := client.Call(ctx, "AddThing", []any{"a"}, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	retried := callspec.WithRequest(t.Context(), "req-1")
	if first, again := call(retried), call(retried); first != 1 || again != 1 {
		t.Fatalf("results %d and %d, want the first result (1) twice", first, again)
	}
	if call(t.Context()) != 2 {
		t.Fatal("a call without a request ID did not run")
	}
	rows := countRows(t, db)
	mu.Lock()
	defer mu.Unlock()
	if rows != 2 || events != 2 || writes != 2 {
		t.Fatalf("%d rows, %d events, %d runs; want 2 of each (the retry adds none)", rows, events, writes)
	}
}
