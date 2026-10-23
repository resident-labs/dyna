package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workeroutput"
)

const spoolSession = "ses_20261021-000000-00000001"

func spoolFixture(t *testing.T) (*runnerSpool, *subagent.Manager) {
	t.Helper()
	dataDir := storetest.DataDir(t)
	if err := state.MigrateRequests(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dataDir) })
	sessions := filepath.Join(dataDir, "sessions")
	recordtest.Write(t, sessions, spoolSession, subagent.Meta{ID: spoolSession, Name: "runner", RoomID: "room_20261021-000000-00000001", Room: "work", Turns: 1})
	m := &subagent.Manager{Store: &subagent.Store{Root: sessions}}
	s, err := newRunnerSpool(dataDir, func() *subagent.Manager { return m }, state.For(db), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	return s, m
}

func writeSpool(t *testing.T, s *runnerSpool, lines ...engineclient.SpoolLine) {
	t.Helper()
	writeSpoolFor(t, s, spoolSession, lines...)
}

func writeSpoolFor(t *testing.T, s *runnerSpool, session string, lines ...engineclient.SpoolLine) {
	t.Helper()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		raw, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(s.dir, session+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func arg(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A write the runner spooled lands before a live write from the same
// session: the engine applies the spool before the live call.
func TestSpooledWriteLandsBeforeALiveOne(t *testing.T) {
	s, m := spoolFixture(t)
	writeSpool(t, s, engineclient.SpoolLine{ID: spoolSession + "/1", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 111)}, At: time.Now()})
	// The served table, without the socket-only check, which needs a socket.
	bindings := RunnerBindings(func() *subagent.Manager { return m })
	for i := range bindings {
		bindings[i].Local = false
	}
	calls, err := websvc.Bind(bindings)
	if err != nil {
		t.Fatal(err)
	}
	s.guard(calls)
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: spoolSession})
	if _, err := calls["RunnerSetPid"](ctx, []json.RawMessage{arg(t, 222)}); err != nil {
		t.Fatal(err)
	}
	meta, err := m.Store.ReadMeta(spoolSession)
	if err != nil || meta.Pid != 222 {
		t.Fatalf("pid = %d, %v; want the live 222 after the spooled 111", meta.Pid, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, spoolSession+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the applied spool file is still there: %v", err)
	}
}

// The engine stops after applying a spool but before removing it, and the
// next drain replays it. The usage it adds is counted once.
func TestReplayedSpoolWritesNothingTwice(t *testing.T) {
	s, m := spoolFixture(t)
	line := engineclient.SpoolLine{ID: spoolSession + "/finish", Method: "RunnerFinish", Args: []json.RawMessage{arg(t, subagent.RunnerFinish{Turn: 1, Usage: workeroutput.Usage{InputTokens: 10}})}, At: time.Now()}
	for range 2 {
		writeSpool(t, s, line)
		if err := s.drain(context.Background(), spoolSession); err != nil {
			t.Fatal(err)
		}
	}
	if meta, err := m.Store.ReadMeta(spoolSession); err != nil || meta.Usage.InputTokens != 10 {
		t.Fatalf("usage = %d, %v; want 10, counted once", meta.Usage.InputTokens, err)
	}
}

func TestAccountSwitchLiveAndSpoolReplayPostsOnce(t *testing.T) {
	s, m := spoolFixture(t)
	data := filepath.Dir(m.Store.Root)
	m.Rooms = &room.Store{Root: filepath.Join(data, "rooms")}
	rec, err := m.Rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.UpdateMeta(spoolSession, func(meta *subagent.Meta) { meta.RoomID, meta.Room = rec.ID, rec.Name }); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.ReadTurn(spoolSession, 1); err == nil {
		t.Fatal("fixture already has a turn")
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_turns(session, n, revision, turn) VALUES (?, 1, 1, ?)`, spoolSession, []byte(arg(t, subagent.Turn{N: 1, Status: subagent.TurnRunning}))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.Store.Root, spoolSession, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	id := spoolSession + "/1/account"
	note := subagent.TurnNote{Turn: 1, AccountSwitch: &harness.AccountSwitch{From: "work", To: "personal"}}
	args := []json.RawMessage{arg(t, note)}
	ctx := callspec.WithRequest(authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: spoolSession}), id)
	if _, err := s.once.run(ctx, runnerRequestKey(id), "RunnerTurnNote", s.calls["RunnerTurnNote"], args); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		writeSpool(t, s, engineclient.SpoolLine{ID: id, Method: "RunnerTurnNote", Args: args, At: time.Now()})
		if err := s.drain(t.Context(), spoolSession); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := m.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, msg := range msgs {
		if msg.Kind == "session" && msg.Notice.Kind == "account" {
			notes++
			if msg.Wake || len(msg.To) != 1 || msg.To[0] != "work:root" {
				t.Fatalf("account notice = %+v", msg)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("live call and two spool replays posted %d notices, want 1", notes)
	}
}

// A line that is not a runner call, one the call refuses, one older than
// request-ID retention and one whose request ID names another session move
// to the rejected file; the rest of the spool
// still applies, and nothing stays in front of later calls.
func TestSpoolRejectsBadLinesAndAppliesTheRest(t *testing.T) {
	s, m := spoolFixture(t)
	finish := arg(t, subagent.RunnerFinish{Turn: 1, Usage: workeroutput.Usage{InputTokens: 10}})
	writeSpool(t, s,
		engineclient.SpoolLine{ID: spoolSession + "/post", Method: "Post", Args: []json.RawMessage{arg(t, "hi")}, At: time.Now()},
		engineclient.SpoolLine{ID: spoolSession + "/bad", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, "not a pid")}, At: time.Now()},
		engineclient.SpoolLine{ID: spoolSession + "/old", Method: "RunnerFinish", Args: []json.RawMessage{finish}, At: time.Now().Add(-state.RequestKeep)},
		engineclient.SpoolLine{ID: "ses_20261021-000000-00000009/other", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 444)}, At: time.Now()},
		engineclient.SpoolLine{ID: spoolSession + "/good", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 333)}, At: time.Now()},
	)
	if err := s.drain(context.Background(), spoolSession); err != nil {
		t.Fatal(err)
	}
	meta, err := m.Store.ReadMeta(spoolSession)
	if err != nil || meta.Pid != 333 || meta.Usage.InputTokens != 0 {
		t.Fatalf("pid %d, usage %d, %v; want the good line applied and the old one not", meta.Pid, meta.Usage.InputTokens, err)
	}
	rejected, err := os.ReadFile(filepath.Join(s.dir, spoolSession+".rejected.jsonl"))
	if err != nil || strings.Count(string(rejected), "\n") != 4 {
		t.Fatalf("rejected lines: %q, %v; want the four bad ones", rejected, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, spoolSession+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the spool file is still there: %v", err)
	}
}

// A runner of the session spools a line while the engine drains its spool.
// The drain took the file before reading it, so the new line goes to a new
// spool file, and the next drain applies it: nothing is removed unread.
func TestSpoolLineAddedDuringADrainIsKept(t *testing.T) {
	s, m := spoolFixture(t)
	writeSpool(t, s, engineclient.SpoolLine{ID: spoolSession + "/1", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 111)}, At: time.Now()})
	s.afterRead = func() {
		s.afterRead = nil
		writeSpool(t, s, engineclient.SpoolLine{ID: spoolSession + "/2", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 222)}, At: time.Now()})
	}
	if err := s.drain(context.Background(), spoolSession); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, spoolSession+".jsonl")); err != nil {
		t.Fatalf("the line spooled during the drain is gone: %v", err)
	}
	if err := s.drain(context.Background(), spoolSession); err != nil {
		t.Fatal(err)
	}
	if meta, err := m.Store.ReadMeta(spoolSession); err != nil || meta.Pid != 222 {
		t.Fatalf("pid = %d, %v; want the later line's 222", meta.Pid, err)
	}
}

// While a runner holds its spool lock, as it does around an append, a drain
// does not take the file: a rename never falls between the append's open
// and its write. Once the lock is free, the drain applies the file.
func TestDrainWaitsForTheRunnersSpoolLock(t *testing.T) {
	s, m := spoolFixture(t)
	writeSpool(t, s, engineclient.SpoolLine{ID: spoolSession + "/1", Method: "RunnerSetPid", Args: []json.RawMessage{arg(t, 555)}, At: time.Now()})
	unlock, err := sessionlife.LockRunnerSpool(context.Background(), s.dir, spoolSession)
	if err != nil {
		t.Fatal(err)
	}
	held, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.drain(held, spoolSession); err == nil {
		t.Fatal("the drain took the spool file while the runner held its lock")
	}
	if _, err := os.Stat(filepath.Join(s.dir, spoolSession+".jsonl")); err != nil {
		t.Fatalf("the spool file moved while the lock was held: %v", err)
	}
	unlock()
	if err := s.drain(context.Background(), spoolSession); err != nil {
		t.Fatal(err)
	}
	if meta, err := m.Store.ReadMeta(spoolSession); err != nil || meta.Pid != 555 {
		t.Fatalf("pid = %d, %v; want 555 once the lock was free", meta.Pid, err)
	}
}
