package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
)

func quotaSpool(t *testing.T, f *museQuotaFixture) (*runnerSpool, *subagent.Manager, *[]string) {
	t.Helper()
	dir := filepath.Dir(f.store.Root)
	m := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(dir, "sessions")}}
	r, err := m.RoomStore().Create(room.CreateInput{Name: "quota"})
	if err != nil {
		t.Fatal(err)
	}
	id := f.input.Session
	if err := sessionlife.Save(m.Store.Root, id, subagent.Meta{ID: id, RoomID: r.ID, Room: r.Name, Turns: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.Store.Root, id, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	var logs []string
	s, err := newRunnerSpool(dir, func() *subagent.Manager { return m }, state.For(f.db), func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	if err != nil {
		t.Fatal(err)
	}
	return s, m, &logs
}

func TestSpooledQuotaReplaysTerminalAccountAAfterAccountB(t *testing.T) {
	f := newMuseQuotaFixture(t)
	s, m, logs := quotaSpool(t, f)
	// The native runner has switched to B before A's delayed observation arrives.
	var b accountstore.Metadata
	if err := f.store.Update(func(st *accountstore.State) error {
		var err error
		b, err = st.Save("muse", "", "identity-b", "B", accountstore.Credential{Schema: "fixture", Payload: json.RawMessage(`{"token":"b"}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetActive("muse", b.ID); err != nil {
		t.Fatal(err)
	}
	second := f.input.ExecutionRef
	second.AdmissionID = f.input.Session + "/native/b"
	if err := state.For(f.db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := attempts.SaveTx(t.Context(), conn, attempts.Fact{ExecutionRef: second, AccountID: b.ID, Identity: b.Identity, Profile: "muse", Harness: "muse", WorkDir: "/repo", AdmittedAt: f.input.ObservedAt})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id := f.input.Session + "/quota/a"
	if err := f.send(id, f.input); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	secrets := quotaSecretSnapshot(t, f.store.Root)
	line := engineclient.SpoolLine{ID: id, Method: "ObserveMuseQuota", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	end := engineclient.SpoolLine{ID: f.input.Session + "/end", Method: "RunnerTurnEnd", Args: []json.RawMessage{arg(t, subagent.TurnEnd{Turn: 1, Status: subagent.TurnStopped, Error: "quota replayed", Ended: time.Now()})}, At: time.Now()}
	for range 2 {
		writeSpoolFor(t, s, f.input.Session, line, end)
		if err := s.drain(t.Context(), f.input.Session); err != nil {
			t.Fatal(err)
		}
	}
	if len(*logs) != 0 {
		t.Fatalf("committed quota rejected: %v", *logs)
	}
	after, err := f.store.Read()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("receipt replay changed accounts: %v", err)
	}
	if !reflect.DeepEqual(secrets, quotaSecretSnapshot(t, f.store.Root)) {
		t.Fatal("receipt replay changed credentials")
	}
	if after.Find(b.ID).Observation != nil {
		t.Fatal("A's observation was attributed to active B")
	}
	if after.Find(before.Entries[0].ID).Observation == nil {
		t.Fatal("A lost its observation")
	}
	turn, err := m.Store.ReadTurn(f.input.Session, 1)
	if err != nil || turn.Error != "quota replayed" {
		t.Fatalf("later terminal write lost: %+v %v", turn, err)
	}
}

func TestSpooledQuotaRejectsConflictsAndForeignReferences(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "changed-observation"
		if foreign {
			name = "foreign-execution"
		}
		t.Run(name, func(t *testing.T) {
			f := newMuseQuotaFixture(t)
			s, _, logs := quotaSpool(t, f)
			id := f.input.Session + "/quota/a"
			if err := f.send(id, f.input); err != nil {
				t.Fatal(err)
			}
			before, err := f.store.Read()
			if err != nil {
				t.Fatal(err)
			}
			changed := f.input
			if foreign {
				changed.Session = "ses_other"
			} else {
				changed.ResetsAt = changed.ResetsAt.Add(time.Hour)
			}
			line := engineclient.SpoolLine{ID: id, Method: "ObserveMuseQuota", Args: []json.RawMessage{arg(t, changed)}, At: time.Now()}
			writeSpoolFor(t, s, f.input.Session, line)
			if err := s.drain(t.Context(), f.input.Session); err != nil {
				t.Fatal(err)
			}
			if len(*logs) != 1 || !strings.HasPrefix((*logs)[0], "bug:") || !strings.Contains((*logs)[0], "request ID was reused") {
				t.Fatalf("conflict did not keep defect log: %v", *logs)
			}
			after, err := f.store.Read()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("conflict changed accounts: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(s.dir, f.input.Session+".rejected.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var rejected engineclient.SpoolLine
			if err := json.Unmarshal(raw, &rejected); err != nil || rejected.ID != line.ID || !reflect.DeepEqual(rejected.Args, line.Args) {
				t.Fatalf("original conflict lost: %s %v", raw, err)
			}
		})
	}
}

func TestUncommittedSpooledQuotaRetainsOriginalAndDrainsTerminal(t *testing.T) {
	f := newMuseQuotaFixture(t)
	s, m, logs := quotaSpool(t, f)
	id := f.input.Session + "/quota/uncommitted"
	line := engineclient.SpoolLine{ID: id, Method: "ObserveMuseQuota", Args: []json.RawMessage{arg(t, f.input)}, At: time.Now()}
	writeSpoolFor(t, s, f.input.Session, line, engineclient.SpoolLine{ID: f.input.Session + "/end", Method: "RunnerTurnEnd", Args: []json.RawMessage{arg(t, subagent.TurnEnd{Turn: 1, Status: subagent.TurnStopped, Error: "uncommitted quota skipped", Ended: time.Now()})}, At: time.Now()})
	if err := s.drain(t.Context(), f.input.Session); err != nil {
		t.Fatal(err)
	}
	if len(*logs) != 1 || !strings.HasPrefix((*logs)[0], "runner spool of ") || !strings.Contains((*logs)[0], "without a committed receipt") {
		t.Fatalf("expected uncertainty logged as a bug: %v", *logs)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, f.input.Session+".rejected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rejected engineclient.SpoolLine
	if err := json.Unmarshal(raw, &rejected); err != nil || rejected.ID != line.ID || !reflect.DeepEqual(rejected.Args, line.Args) {
		t.Fatalf("original observation lost: %s %v", raw, err)
	}
	if _, done, err := state.For(f.db).RequestDone(t.Context(), runnerRequestKey(id)); err != nil || done {
		t.Fatalf("invented receipt: %v %v", done, err)
	}
	st, err := f.store.Read()
	if err != nil || st.Entries[0].Observation != nil {
		t.Fatalf("fresh offline observation applied: %+v %v", st, err)
	}
	turn, err := m.Store.ReadTurn(f.input.Session, 1)
	if err != nil || turn.Error != "uncommitted quota skipped" {
		t.Fatalf("later terminal write lost: %+v %v", turn, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, f.input.Session+".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drained spool remains: %v", err)
	}
}
