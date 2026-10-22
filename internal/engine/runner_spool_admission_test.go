package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

func admissionSpool(t *testing.T, f *admissionFixture) *runnerSpool {
	t.Helper()
	m := f.s.rooms.Notify.Sessions
	rec, err := m.RoomStore().Create(room.CreateInput{Name: "spool"})
	if err != nil {
		t.Fatal(err)
	}
	meta := f.meta
	meta.RoomID, meta.Room = rec.ID, rec.Name
	if err := sessionlife.Save(m.Store.Root, meta.ID, meta); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.Store.Root, meta.ID, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := newRunnerSpool(f.dir, func() *subagent.Manager { return f.s.rooms.Notify.Sessions }, state.For(f.db), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSpooledAdmissionReplaysOnlyItsCommittedReceipt(t *testing.T) {
	f := newAdmissionFixture(t)
	id := f.meta.ID + "/admission"
	ref, err := f.call(id, f.input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := attempts.Load(t.Context(), f.dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE session_turns SET turn=json_set(CAST(turn AS TEXT),'$.status',?) WHERE session=? AND n=1", subagent.TurnStopped, f.meta.ID); err != nil {
		t.Fatal(err)
	}
	s := admissionSpool(t, f)
	line := engineclient.SpoolLine{ID: id, Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	end := engineclient.SpoolLine{ID: f.meta.ID + "/end", Method: "RunnerTurnEnd", Args: []json.RawMessage{arg(t, subagent.TurnEnd{Turn: 1, Status: subagent.TurnStopped, Error: "receipt recovered", Ended: time.Now()})}, At: time.Now()}
	finish := engineclient.SpoolLine{ID: f.meta.ID + "/finish", Method: "RunnerFinish", Args: []json.RawMessage{arg(t, subagent.RunnerFinish{Turn: 1, Usage: workeroutput.Usage{InputTokens: 2}})}, At: time.Now()}
	for range 2 {
		writeSpoolFor(t, s, f.meta.ID, line, end, finish)
		if err := s.drain(t.Context(), f.meta.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, f.meta.ID+".rejected.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed admission was rejected during recovery: %v", err)
	}
	after, err := attempts.Load(t.Context(), f.dir, ref)
	if err != nil || after != before {
		t.Fatalf("recovery changed the immutable fact: %+v %v", after, err)
	}
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM account_attempts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovery left %d facts: %v", count, err)
	}
	turn, err := f.s.rooms.Notify.Sessions.Store.ReadTurn(f.meta.ID, 1)
	if err != nil || turn.Error != "receipt recovered" {
		t.Fatalf("terminal write after receipt replay did not apply: %+v %v", turn, err)
	}
	meta, err := f.s.rooms.Notify.Sessions.Store.ReadMeta(f.meta.ID)
	if err != nil || meta.Usage.InputTokens != 2 {
		t.Fatalf("runner finish after replay did not apply exactly once: %+v %v", meta, err)
	}
}

func TestSpooledAdmissionStillRejectsChangedArguments(t *testing.T) {
	f := newAdmissionFixture(t)
	id := f.meta.ID + "/admission"
	ref, err := f.call(id, f.input)
	if err != nil {
		t.Fatal(err)
	}
	s := admissionSpool(t, f)
	var reasons []string
	s.logf = func(format string, args ...any) { reasons = append(reasons, fmt.Sprintf(format, args...)) }
	in := f.input
	in.AccountID = "changed"
	writeSpoolFor(t, s, f.meta.ID, engineclient.SpoolLine{ID: id, Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, in)}, At: time.Now()})
	if err := s.drain(t.Context(), f.meta.ID); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "request ID was reused with different method or arguments") {
		t.Fatalf("changed admission did not meet the fingerprint check: %v", reasons)
	}
	if !strings.HasPrefix(reasons[0], "bug:") {
		t.Fatalf("a real changed-argument defect lost its bug log: %v", reasons)
	}
	fact, err := attempts.Load(t.Context(), f.dir, ref)
	if err != nil || fact.AccountID != f.input.AccountID {
		t.Fatalf("changed spool rewrote the admitted account: %+v %v", fact, err)
	}
}

func TestUncommittedSpooledAdmissionCannotBlockTerminalWrites(t *testing.T) {
	f := newAdmissionFixture(t)
	s := admissionSpool(t, f)
	var reasons []string
	s.logf = func(format string, args ...any) { reasons = append(reasons, fmt.Sprintf(format, args...)) }
	id := f.meta.ID + "/unacknowledged"
	writeSpoolFor(t, s, f.meta.ID,
		engineclient.SpoolLine{ID: id, Method: "AdmitNativeAttempt", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()},
		engineclient.SpoolLine{ID: f.meta.ID + "/end", Method: "RunnerTurnEnd", Args: []json.RawMessage{arg(t, subagent.TurnEnd{Turn: 1, Status: subagent.TurnStopped, Ended: time.Now()})}, At: time.Now()},
		engineclient.SpoolLine{ID: f.meta.ID + "/finish", Method: "RunnerFinish", Args: []json.RawMessage{arg(t, subagent.RunnerFinish{Turn: 1, Usage: workeroutput.Usage{InputTokens: 2}})}, At: time.Now()},
	)
	if err := s.drain(t.Context(), f.meta.ID); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "offline recovery cannot apply a call without a committed receipt") {
		t.Fatalf("missing receipt was not refused as an offline admission: %v", reasons)
	}
	if !strings.HasPrefix(reasons[0], "runner spool of ") {
		t.Fatalf("an expected uncommitted admission was logged as a bug: %v", reasons)
	}
	ref := f.input.ExecutionRef
	ref.AdmissionID = id
	if _, err := attempts.Load(t.Context(), f.dir, ref); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("offline recovery created an admission: %v", err)
	}
	if _, done, err := state.For(f.db).RequestDone(t.Context(), runnerRequestKey(id)); err != nil || done {
		t.Fatalf("offline recovery invented an acknowledgement: %v %v", done, err)
	}
	turn, err := f.s.rooms.Notify.Sessions.Store.ReadTurn(f.meta.ID, 1)
	if err != nil || turn.Status != subagent.TurnStopped {
		t.Fatalf("later terminal turn did not apply: %+v %v", turn, err)
	}
	meta, err := f.s.rooms.Notify.Sessions.Store.ReadMeta(f.meta.ID)
	if err != nil || meta.Usage.InputTokens != 2 {
		t.Fatalf("later runner finish did not apply: %+v %v", meta, err)
	}
}
