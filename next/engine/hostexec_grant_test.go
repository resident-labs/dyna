package engine

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
	"strings"
	"testing"
	"time"
)

func TestRootDefaultGrant(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	g, err := s.store.ListActiveGrants(ctx, room, contract.Principal{Kind: contract.PrincipalSession, Session: root}, s.now())
	if err != nil || len(g) != 1 || g[0].Hosts[0] != contract.AllHosts || !g[0].Expires.IsZero() {
		t.Fatalf("root grant %+v: %v", g, err)
	}
}
func TestGrantOperation(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	target := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	a := contract.HostexecGrantArgs{Room: room, To: target, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}
	res, problem := s.OpSubmit(ctx, owner, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: a}})
	if problem != nil || res.Operation.Class != contract.ClassOwner || res.Operation.State != contract.OpDone {
		t.Fatalf("result %+v %v", res, problem)
	}
	g, err := s.store.ListActiveGrants(ctx, room, target, s.now())
	if err != nil || len(g) != 1 || !g[0].Expires.Equal(s.now().Add(24*time.Hour)) {
		t.Fatalf("grant %+v %v", g, err)
	}
}

func TestGrantCard(t *testing.T) {
	s, room, worker, ctx := setup(t)
	to := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if err := s.store.SetEngineHost(ctx, "host", false); err != nil {
		t.Fatal(err)
	}
	card := s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}, to)
	if card.Title != "asks for host access" || card.Target != (contract.Target{Kind: contract.TargetHost, Name: "host"}) || card.Body != "Run any command on host for 24 hours.\nReason: Inspect logs" {
		t.Fatalf("card: %+v", card)
	}
	// The owner granting another session names it, and two hosts keep the room as the target.
	card = s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"a", "b"}, Reason: "Inspect logs"}, contract.Principal{Kind: contract.PrincipalOwner})
	if card.Target.Kind != contract.TargetRoom || !strings.HasPrefix(card.Body, "Let ") || !strings.Contains(card.Body, " run any command on a, b for 24 hours.") {
		t.Fatalf("card: %+v", card)
	}
	for expires, want := range map[time.Duration]string{time.Hour: "for up to 1 hour.", 2*time.Hour + time.Second: "for up to 3 hours."} {
		card = s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Hosts: []contract.HostName{"host"}, Reason: "x", Expires: s.now().Add(expires)}, contract.Principal{})
		if !strings.Contains(card.Body, want) {
			t.Fatalf("card: %q, want %q", card.Body, want)
		}
	}
}

func TestUpgradeRemovesLegacyGrantRequestFromList(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('room','room',1,1,1);
 DROP TABLE host_rooms;
 ALTER TABLE pr_owners DROP COLUMN branch;
 DROP INDEX events_client_id;
 ALTER TABLE events DROP COLUMN client_id;
 ALTER TABLE hosts DROP COLUMN maintained_at;
 ALTER TABLE hosts DROP COLUMN engine_host;
 ALTER TABLE turns DROP COLUMN stop_requested_at;
 ALTER TABLE sessions DROP COLUMN shell_jobs;
 ALTER TABLE sessions DROP COLUMN model_retry_at;
 ALTER TABLE sessions DROP COLUMN model_retries;
 ALTER TABLE sessions DROP COLUMN close_reason;
 ALTER TABLE boxes ADD COLUMN parent TEXT REFERENCES boxes(id);
 ALTER TABLE boxes DROP COLUMN want;
 ALTER TABLE boxes DROP COLUMN due_at;
 ALTER TABLE boxes DROP COLUMN failures;
 ALTER TABLE grants ADD COLUMN mode TEXT NOT NULL DEFAULT '';
 ALTER TABLE grants ADD COLUMN commands BLOB NOT NULL DEFAULT '[]';
 ALTER TABLE grants DROP COLUMN reason;
 DROP TABLE conversation_reads;
 DROP TABLE rules;
 DROP TABLE uploads;
 DROP TABLE backup_copy_status;
 DROP TABLE device_scopes;
 DROP TABLE devices;
 DROP TABLE pairing_codes;
 UPDATE meta SET value='17' WHERE key='schema_version';
 INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES
 ('old','hostexec.grant','room','session','worker','owner','pending','{}',CAST('{"room":"room","to":{"kind":"session","session":"worker"},"hosts":["host"],"commands":[{"program":"git"}],"mode":"approve"}' AS BLOB),x'01',1,9999999999999),
 ('new','hostexec.grant','room','session','worker','owner','pending','{}',CAST('{"room":"room","to":{"kind":"session","session":"worker"},"hosts":["host"],"reason":"Inspect logs"}' AS BLOB),x'02',2,9999999999999);`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, fakeBoxes{}, fakeProfiles{}, time.Now)
	if err = s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	list, problem := s.OpList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.OpListRequest{Room: "room"})
	if problem != nil || len(list.Operations) != 1 || list.Operations[0].ID != "new" {
		t.Fatalf("list %+v: %+v", list, problem)
	}
	old, err := st.Operation(ctx, "old")
	if err != nil || old.Operation.State != contract.OpExpired {
		t.Fatalf("old request %+v: %v", old, err)
	}
}

func TestHostGrantAfterEngineMove(t *testing.T) {
	s, room, worker, ctx := setup(t)
	no := false
	if err := s.store.AddHost(ctx, box.Host{Name: "capacity", EngineHost: &no}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.grant": s.HostexecGrant()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	args := contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"capacity"}, Reason: "Inspect"}
	result, problem := s.OpSubmit(ctx, to, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "hostexec.grant", Args: args}})
	if problem != nil || result.Operation.State != contract.OpPending {
		t.Fatalf("submit: %+v %v", result, problem)
	}
	// Startup clears comparisons and expires cards made before the engine moved.
	if err := s.store.ResetEngineHosts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireHostexecGrantRequests(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := s.store.Operation(ctx, result.Operation.ID)
	if err != nil || old.Operation.State != contract.OpExpired {
		t.Fatalf("old request: %+v %v", old, err)
	}
	card := s.HostexecGrant().Card(ctx, args, to)
	if !strings.Contains(card.Body, "may hold the model sign-ins. Approve only if you trust this session with them.") {
		t.Fatal(card.Body)
	}
}

func TestGrantCardEngineHost(t *testing.T) {
	s, room, worker, ctx := setup(t)
	yes := true
	if err := s.store.AddHost(ctx, box.Host{Name: "engine", EngineHost: &yes}); err != nil {
		t.Fatal(err)
	}
	to := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	card := s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: to, Hosts: []contract.HostName{"engine"}, Reason: "Inspect"}, to)
	if !strings.Contains(card.Body, "This host also runs the engine and holds the model sign-ins.") {
		t.Fatal(card.Body)
	}
}
