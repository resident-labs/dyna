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
	grant := contract.Grant{ID: "grant", Kind: contract.GrantHostExec, Room: "room", To: who, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: now, Mode: contract.GrantModePreapproved, Hosts: []contract.HostName{"host"}, Commands: []contract.CommandRule{{Program: "true"}}}
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
	var ended int64
	if err = s.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op='running'").Scan(&stopped, &ended); err != nil || stopped != "lost" || ended != recovered.UnixMilli() {
		t.Fatalf("running %q %d: %v", stopped, ended, err)
	}
	if err = s.Read().QueryRow("SELECT stopped,ended_at FROM hostexec_runs WHERE op='finished'").Scan(&stopped, &ended); err != nil || stopped != "revoked" || ended != now.UnixMilli() {
		t.Fatalf("finished %q %d: %v", stopped, ended, err)
	}
	runs, err := s.HostexecStops(ctx, recovered)
	if err != nil || len(runs) != 1 || runs[0].Op != "running" {
		t.Fatalf("stop sweep %+v: %v", runs, err)
	}
}
