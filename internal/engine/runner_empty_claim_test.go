package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/turnlock"
	"dyna-agent/internal/websvc"
)

type emptyClaimFixture struct {
	db         *sql.DB
	w          *state.Writer
	manager    *subagent.Manager
	room       room.Record
	meta       subagent.Meta
	dir        string
	sessionDir string
	next       int
}

func newEmptyClaimFixture(t *testing.T) *emptyClaimFixture {
	t.Helper()
	dir := storetest.Isolate(t)
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(dir) })
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rooms := &room.Store{Root: filepath.Join(dir, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(dir, "sessions")
	meta := subagent.Meta{ID: "ses_empty_claim", Name: "reader", RoomID: rec.ID, Room: rec.Name,
		Turns: 1, Depth: 1, Status: subagent.StatusRunning}
	recordtest.Write(t, sessions, meta.ID, meta)
	turn, err := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES(?,?,?,?)", meta.ID, 1, 1, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_queues(session,revision,queue) VALUES(?,?,?)", meta.ID, 1, `{"claimed":{},"cancelled":{},"dismissed":{}}`); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(sessions, meta.ID)
	gate := fmt.Sprintf(`{"active":true,"pid":%d,"turn":1}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(sessionDir, turnlock.GateFile), []byte(gate), 0600); err != nil {
		t.Fatal(err)
	}
	manager := &subagent.Manager{Store: &subagent.Store{Root: sessions}, Rooms: rooms}
	return &emptyClaimFixture{db: db, w: state.For(db), manager: manager, room: rec, meta: meta, dir: dir, sessionDir: sessionDir}
}

func (f *emptyClaimFixture) claim(t *testing.T, input subagent.InputClaim) (subagent.ClaimedInput, string) {
	t.Helper()
	f.next++
	rid := fmt.Sprintf("%s/1/fixture/%d", f.meta.ID, f.next)
	key := runnerRequestKey(rid)
	ctx := callspec.WithRequest(authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: f.meta.ID}), rid)
	args, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	once := &requestRunner{w: f.w, logf: t.Logf}
	result, err := once.run(ctx, key, "RunnerClaimInput", func(ctx context.Context, _ []json.RawMessage) (any, error) {
		return f.manager.RunnerClaimInput(ctx, input)
	}, []json.RawMessage{args})
	if err != nil {
		t.Fatal(err)
	}
	return result.(subagent.ClaimedInput), key
}

func (f *emptyClaimFixture) post(t *testing.T) room.Message {
	t.Helper()
	msg, err := f.manager.Rooms.Post(f.room.ID, room.PostInput{From: f.room.Name, Human: true, To: []string{f.meta.Address()}, Text: "input to claim"})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestEmptyRunnerClaimsWriteNothing(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(fmt.Sprintf("startup=%v", startup), func(t *testing.T) {
			f := newEmptyClaimFixture(t)
			before := f.w.Health().Writes
			var queueBefore []byte
			var version int64
			if err := f.db.QueryRow("SELECT queue,revision FROM session_queues WHERE session=?", f.meta.ID).Scan(&queueBefore, &version); err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for range 30 {
				got, key := f.claim(t, subagent.InputClaim{Turn: 1, Startup: startup, Depth: f.meta.Depth})
				if len(got.Posts) != 0 || got.Closed {
					t.Fatalf("idle claim was not empty: %+v", got)
				}
				if _, done, err := f.w.RequestDone(t.Context(), key); err != nil {
					t.Fatal(err)
				} else if done {
					receipts++
				}
			}
			if receipts != 0 {
				t.Errorf("30 idle claims retained %d receipts; want zero", receipts)
			}
			if writes := f.w.Health().Writes - before; writes != 0 {
				t.Errorf("30 idle claims made %d SQL writes; want zero", writes)
			}
			var queueAfter []byte
			var afterVersion int64
			if err := f.db.QueryRow("SELECT queue,revision FROM session_queues WHERE session=?", f.meta.ID).Scan(&queueAfter, &afterVersion); err != nil {
				t.Fatal(err)
			}
			if version != afterVersion || !bytes.Equal(queueBefore, queueAfter) {
				t.Fatal("idle claims changed the canonical queue")
			}
		})
	}
}

func TestEmptyRunnerClaimControlsKeepExactReceipts(t *testing.T) {
	for _, kind := range []string{"covered note cancellation", "final gate sealing", "hierarchy input"} {
		t.Run(kind, func(t *testing.T) {
			f := newEmptyClaimFixture(t)
			input := subagent.InputClaim{Turn: 1, Depth: f.meta.Depth}
			var messageID string
			switch kind {
			case "covered note cancellation":
				messageID = f.post(t).ID
				input.Startup, input.Covered = true, []string{messageID}
			case "final gate sealing":
				input.Final = true
			case "hierarchy input":
				input.Depth = 0
			}
			got, key := f.claim(t, input)
			want, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			saved, done, err := f.w.RequestDone(t.Context(), key)
			if err != nil || !done || !bytes.Equal(saved, want) {
				t.Fatalf("control receipt = %s, done %v, error %v; want %s", saved, done, err, want)
			}
			if kind == "final gate sealing" {
				var gate struct {
					Active   bool
					ClosedAt int64
				}
				raw, err := os.ReadFile(filepath.Join(f.sessionDir, turnlock.GateFile))
				if err != nil || json.Unmarshal(raw, &gate) != nil || gate.Active || !got.Closed || gate.ClosedAt != got.Cursor {
					t.Fatalf("final control failed to seal the gate: %s, %+v, %v", raw, got, err)
				}
			} else if kind == "covered note cancellation" {
				var raw []byte
				if err := f.db.QueryRow("SELECT queue FROM session_queues WHERE session=?", f.meta.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var queue struct{ Cancelled map[string]bool }
				if err := json.Unmarshal(raw, &queue); err != nil || !queue.Cancelled[messageID] || len(got.Posts) != 0 {
					t.Fatalf("covered note was not cancelled: %s, %+v, %v", raw, got, err)
				}
			} else if len(got.Posts) != 1 {
				t.Fatalf("hierarchy change did not return control input: %+v", got)
			}
		})
	}
}

func TestInputBearingClaimReceiptSurvivesEmptyReadsAndReopen(t *testing.T) {
	f := newEmptyClaimFixture(t)
	_, emptyKey := f.claim(t, subagent.InputClaim{Turn: 1, Depth: f.meta.Depth})
	message := f.post(t)
	input := subagent.InputClaim{Turn: 1, Depth: f.meta.Depth}
	got, key := f.claim(t, input)
	if len(got.Posts) != 1 || got.Posts[0].Steer.ID != message.ID {
		t.Fatalf("new input was not claimed after an empty read: %+v", got)
	}
	want, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(f.dir); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	w := state.For(reopened)
	if _, done, err := w.RequestDone(t.Context(), emptyKey); err != nil || done {
		t.Fatalf("empty receipt appeared after reopen: %v %v", done, err)
	}
	once := &requestRunner{w: w, logf: t.Logf}
	args, _ := json.Marshal(input)
	before := w.Health().Writes
	replay, err := once.run(t.Context(), key, "RunnerClaimInput", func(context.Context, []json.RawMessage) (any, error) {
		t.Error("lost-reply replay executed the selector")
		return nil, fmt.Errorf("unexpected claim")
	}, []json.RawMessage{args})
	raw, marshalErr := json.Marshal(replay)
	if err != nil || marshalErr != nil || !bytes.Equal(raw, want) || w.Health().Writes != before {
		t.Fatalf("reopened input receipt = %s, %v %v", raw, err, marshalErr)
	}
}

func TestEmptyClaimIDCanReadNewInput(t *testing.T) {
	f := newEmptyClaimFixture(t)
	input := subagent.InputClaim{Turn: 1, Depth: f.meta.Depth}
	_, key := f.claim(t, input)
	message := f.post(t)
	args, _ := json.Marshal(input)
	ctx := callspec.WithRequest(authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: f.meta.ID}), key[len("runner/"):])
	once := &requestRunner{w: f.w, logf: t.Logf}
	result, err := once.run(ctx, key, "RunnerClaimInput", func(ctx context.Context, _ []json.RawMessage) (any, error) {
		return f.manager.RunnerClaimInput(ctx, input)
	}, []json.RawMessage{args})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(subagent.ClaimedInput)
	if len(got.Posts) != 1 || got.Posts[0].Steer.ID != message.ID {
		t.Fatalf("an empty result prevented later input: %+v", got)
	}
	raw, done, err := f.w.RequestDone(t.Context(), key)
	want, _ := json.Marshal(got)
	if err != nil || !done || !bytes.Equal(raw, want) {
		t.Fatalf("later input lost its exact receipt: %s %v %v", raw, done, err)
	}
}

func TestWireCannotSuppressInputClaimReceipt(t *testing.T) {
	f := newEmptyClaimFixture(t)
	calls, err := websvc.Bind(RunnerBindings(func() *subagent.Manager { return f.manager }))
	if err != nil {
		t.Fatal(err)
	}
	applyOnce(calls, f.w, t.Logf)
	callers := &socketCallers{sessions: f.manager.Store, enforce: true, logf: t.Logf}
	callers.actForCallers(calls)
	path := enginetest.ServeCallers(t, f.dir, func(net.Conn) (websvc.Caller, error) {
		return websvc.Caller{Kind: websvc.Session, Session: f.meta.ID}, nil
	}, calls)
	client := engineclient.New(path)
	input := map[string]any{"turn": 1, "depth": 1, "readOnly": true, "requestReadOnly": true}
	emptyID := f.meta.ID + "/wire/empty"
	before := f.w.Health().Writes
	var empty json.RawMessage
	if err := client.Call(callspec.WithRequest(t.Context(), emptyID), "RunnerClaimInput", []any{input}, &empty); err != nil {
		t.Fatal(err)
	}
	if f.w.Health().Writes != before || bytes.Contains(empty, []byte("readOnly")) || bytes.Contains(empty, []byte("requestReadOnly")) {
		t.Fatalf("wire empty response wrote state or exposed its private decision: %s", empty)
	}
	if _, done, err := f.w.RequestDone(t.Context(), runnerRequestKey(emptyID)); err != nil || done {
		t.Fatalf("wire empty response retained a receipt: %v %v", done, err)
	}

	message := f.post(t)
	inputID := f.meta.ID + "/wire/input"
	var got subagent.ClaimedInput
	if err := client.Call(callspec.WithRequest(t.Context(), inputID), "RunnerClaimInput", []any{input}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Posts) != 1 || got.Posts[0].Steer.ID != message.ID {
		t.Fatalf("wire flags changed the actual claim: %+v", got)
	}
	saved, done, err := f.w.RequestDone(t.Context(), runnerRequestKey(inputID))
	if err != nil || !done || len(saved) == 0 {
		t.Fatalf("wire flag suppressed the exact receipt: %s %v %v", saved, done, err)
	}
	var decoded subagent.ClaimedInput
	if err := json.Unmarshal([]byte(`{"cursor":0,"readOnly":true,"requestReadOnly":true}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if marker, ok := any(decoded).(interface{ RequestReadOnly() bool }); ok && marker.RequestReadOnly() {
		t.Fatal("wire response forged the selector's private receipt decision")
	}
}
