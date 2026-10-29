package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
)

// This fixture performs only successful SQL writes, with no JSONL appends.
// Its Writer.Health delta therefore counts the transactions in this workload.
func TestRequestFinalizationSQLCounts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		method   string
		mutation bool
		saved    []byte
		reply    any
		commits  int
	}{
		{"atomic response", "CommitThing", true, []byte(`"reply"`), "reply", 1},
		{"empty claim response", "RunnerClaimInput", false, []byte(`{"cursor":7}`), subagent.ClaimedInput{Cursor: 7}, 1},
		{"atomic null response", "CommitThing", true, []byte("null"), nil, 1},
		{"different JSON bytes", "CommitThing", false, []byte(` { "cursor" : 7 } `), subagent.ClaimedInput{Cursor: 7}, 2},
		{"mutation-only receipt", "CommitThing", true, nil, "reply", 2},
		{"mutation-only null response", "CommitThing", true, nil, nil, 1},
		{"changed response", "CommitThing", true, []byte(`"earlier"`), "reply", 2},
		{"first receipt without mutation", "CommitThing", false, nil, "reply", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, w, _ := thingsStore(t)
			executions := 0
			call := func(ctx context.Context, _ []json.RawMessage) (any, error) {
				executions++
				if !tc.mutation && tc.saved == nil {
					return tc.reply, nil
				}
				err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
					if tc.mutation {
						if _, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES('once')"); err != nil {
							return err
						}
					}
					if tc.saved != nil {
						return w.FinishRequestIn(ctx, conn, tc.method, tc.saved)
					}
					return nil
				})
				return tc.reply, err
			}
			once := &requestRunner{w: w, logf: t.Logf}
			before := w.Health().Writes
			result, err := once.run(t.Context(), "finalize-1", tc.method, call, nil)
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tc.reply)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(result)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("response = %s, %v; want %s", got, err, want)
			}
			if commits := w.Health().Writes - before; commits != tc.commits {
				t.Errorf("SQL commits = %d, want %d", commits, tc.commits)
			}
			saved, done, err := w.RequestDone(t.Context(), "finalize-1")
			if err != nil || !done || !bytes.Equal(nullIfEmpty(saved), want) {
				t.Fatalf("saved response = %s, done %v, error %v; want %s", saved, done, err, want)
			}
			// A lost reply uses a fresh request runner and still writes nothing.
			beforeReplay := w.Health().Writes
			retry := &requestRunner{w: w, logf: t.Logf}
			replay, err := retry.run(t.Context(), "finalize-1", tc.method, call, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(replay)
			if err != nil || !bytes.Equal(raw, want) || executions != 1 || w.Health().Writes != beforeReplay {
				t.Fatalf("replay = %s, %v; executions %d, writes %d -> %d", raw, err, executions, beforeReplay, w.Health().Writes)
			}
			for _, changed := range []struct {
				method string
				args   []json.RawMessage
			}{
				{tc.method, []json.RawMessage{json.RawMessage(`"different"`)}},
				{"DifferentMethod", nil},
			} {
				if _, err := retry.run(t.Context(), "finalize-1", changed.method, call, changed.args); !errors.Is(err, state.ErrRequestConflict) {
					t.Fatalf("conflicting replay = %v", err)
				}
			}
			if executions != 1 || w.Health().Writes != beforeReplay {
				t.Fatal("conflicting replay executed or wrote")
			}
			wantRows := 0
			if tc.mutation {
				wantRows = 1
			}
			if rows := countRows(t, db); rows != wantRows {
				t.Fatalf("mutation rows = %d, want %d", rows, wantRows)
			}
		})
	}
}

func TestRequestFinalizationLostReplyAfterReopen(t *testing.T) {
	db, w, dir := thingsStore(t)
	call := func(ctx context.Context, _ []json.RawMessage) (any, error) {
		err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES('once')"); err != nil {
				return err
			}
			return w.FinishRequestIn(ctx, conn, "CommitThing", []byte(`{"cursor":9}`))
		})
		return json.RawMessage(`{"cursor":9}`), err
	}
	once := &requestRunner{w: w, logf: t.Logf}
	if _, err := once.run(t.Context(), "reopen-1", "CommitThing", call, nil); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened == db {
		t.Fatal("database was not reopened")
	}
	recovered := state.For(reopened)
	before := recovered.Health().Writes
	retry := &requestRunner{w: recovered, logf: t.Logf}
	never := func(context.Context, []json.RawMessage) (any, error) {
		t.Error("recovered receipt reached the mutation")
		return nil, errors.New("unexpected mutation")
	}
	result, err := retry.run(t.Context(), "reopen-1", "CommitThing", never, nil)
	raw, marshalErr := json.Marshal(result)
	if err != nil || marshalErr != nil || string(raw) != `{"cursor":9}` || recovered.Health().Writes != before || countRows(t, reopened) != 1 {
		t.Fatalf("recovered reply = %s, %v, %v; writes %d -> %d", raw, err, marshalErr, before, recovered.Health().Writes)
	}
}

func TestRequestFinalizationFingerprintConflict(t *testing.T) {
	_, w, _ := thingsStore(t)
	once := &requestRunner{w: w, logf: t.Logf}
	call := func(ctx context.Context, _ []json.RawMessage) (any, error) {
		// Even equal response bytes cannot hide a different argument digest.
		changed := state.ForRequestCall(ctx, "conflict-1", "CommitThing", []byte("different fingerprint"))
		err := w.Write(changed, hub.Event{}, func(conn *sql.Conn) error {
			return w.FinishRequestIn(changed, conn, "CommitThing", []byte(`"reply"`))
		})
		return "reply", err
	}
	if _, err := once.run(t.Context(), "conflict-1", "CommitThing", call, nil); !errors.Is(err, state.ErrRequestConflict) {
		t.Fatalf("matching response hid fingerprint conflict: %v", err)
	}
}

func TestRequestFinalizationLookupFailureKeepsFinishAttempt(t *testing.T) {
	_, w, _ := thingsStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var logs []string
	once := &requestRunner{w: w, logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}}
	call := func(ctx context.Context, _ []json.RawMessage) (any, error) {
		if err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			return w.FinishRequestIn(ctx, conn, "CommitThing", []byte(`"reply"`))
		}); err != nil {
			return nil, err
		}
		// The newly added lookup fails after the mutation has committed.
		cancel()
		return "reply", nil
	}
	before := w.Health().Writes
	result, err := once.run(ctx, "cancel-1", "CommitThing", call, nil)
	if err != nil || result != "reply" {
		t.Fatalf("post-commit cancellation changed the healthy result: %v, %v", result, err)
	}
	// The second attempt is canceled, not a successful SQL commit.
	if attempts := w.Health().Writes - before; attempts != 2 || len(logs) != 1 || !strings.Contains(logs[0], "applied, but its result was not stored") {
		t.Fatalf("finalization attempt or post-commit log was lost: attempts %d, logs %v", attempts, logs)
	}
	raw, done, err := w.RequestDone(t.Context(), "cancel-1")
	if err != nil || !done || string(raw) != `"reply"` {
		t.Fatalf("committed exact receipt lost: %s %v %v", raw, done, err)
	}
}

func TestRequestFinalizationRollbackKeepsRetry(t *testing.T) {
	db, w, _ := thingsStore(t)
	fail := true
	rejected := errors.New("roll back the response and mutation")
	call := func(ctx context.Context, _ []json.RawMessage) (any, error) {
		err := w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, "INSERT INTO things(name) VALUES('once')"); err != nil {
				return err
			}
			if err := w.FinishRequestIn(ctx, conn, "CommitThing", []byte(`"reply"`)); err != nil {
				return err
			}
			if fail {
				return rejected
			}
			return nil
		})
		return "reply", err
	}
	once := &requestRunner{w: w, logf: t.Logf}
	if _, err := once.run(t.Context(), "rollback-1", "CommitThing", call, nil); !errors.Is(err, rejected) {
		t.Fatalf("failed transaction = %v", err)
	}
	if _, done, err := w.RequestDone(t.Context(), "rollback-1"); err != nil || done || countRows(t, db) != 0 {
		t.Fatalf("failed transaction retained state: done %v, error %v", done, err)
	}
	fail = false
	before := w.Health().Writes
	if _, err := once.run(t.Context(), "rollback-1", "CommitThing", call, nil); err != nil {
		t.Fatal(err)
	}
	if writes := w.Health().Writes - before; writes != 1 || countRows(t, db) != 1 {
		t.Fatalf("successful retry used %d writes or repeated mutation", writes)
	}
}
