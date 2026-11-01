package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRecoverHostexecMarksInterruptedRunsLost(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	who := contract.Principal{Kind: contract.PrincipalSession, Session: "session"}
	grant := contract.Grant{ID: "grant", Kind: contract.GrantHostExec, Room: "room", To: who, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: now, Hosts: []contract.HostName{"host"}}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if err := s.InsertGrant(tx, grant); err != nil {
			return err
		}
		for _, id := range []contract.OpID{"running", "finished"} {
			record := StoredOperation{Operation: contract.Operation{ID: id, Kind: "hostexec.run", Room: "room", By: who, Class: contract.ClassAuto, State: contract.OpApproved, Created: now, Expires: now.Add(time.Hour)}, Args: json.RawMessage(`{}`)}
			if err := s.InsertOperation(tx, record, contract.Event{}, nil); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO hostexec_runs(op,grant_id,host,scope,started_at,timeout_ms) VALUES(?,?,?,?,?,?)", id, grant.ID, "host", string(id), now.UnixMilli(), 600000); err != nil {
				return err
			}
		}
		_, err := tx.Exec("UPDATE hostexec_runs SET ended_at=?,stopped='revoked' WHERE op='finished'", now.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered := now.Add(time.Minute)
	if err = s.RecoverHostexec(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	var stopped string
	var ended sql.NullInt64
	if err = s.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op='running'").Scan(&stopped, &ended); err != nil || stopped != "lost" || ended.Valid {
		t.Fatalf("running %q %+v: %v", stopped, ended, err)
	}
	if err = s.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op='finished'").Scan(&stopped, &ended); err != nil || stopped != "revoked" || !ended.Valid || ended.Int64 != now.UnixMilli() {
		t.Fatalf("finished %q %+v: %v", stopped, ended, err)
	}
	runs, err := s.HostexecStops(ctx, recovered)
	if err != nil || len(runs) != 1 || runs[0].Op != "running" {
		t.Fatalf("stop sweep %+v: %v", runs, err)
	}
	if err = s.CompleteHostexecStop(ctx, "running", recovered.Add(29*time.Second)); err != nil {
		t.Fatal(err)
	}
	runs, err = s.HostexecStops(ctx, recovered.Add(29*time.Second))
	if err != nil || len(runs) != 1 {
		t.Fatalf("early stop: %+v %v", runs, err)
	}
	if err = s.CompleteHostexecStop(ctx, "running", recovered.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	runs, err = s.HostexecStops(ctx, recovered.Add(30*time.Second))
	if err != nil || len(runs) != 0 {
		t.Fatalf("completed stop: %+v %v", runs, err)
	}
	if err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE hostexec_runs SET ended_at=NULL WHERE op='running'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runs, err = s.HostexecStops(ctx, recovered.Add(640*time.Second))
	if err != nil || len(runs) != 0 {
		t.Fatalf("expired stop: %+v %v", runs, err)
	}
	if err = s.Read().QueryRow("SELECT ended_at FROM hostexec_runs WHERE op='running'").Scan(&ended); err != nil || !ended.Valid {
		t.Fatalf("deadline: %+v %v", ended, err)
	}
}
