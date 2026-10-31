package engine

import (
	"context"
	"database/sql"
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
	card := s.HostexecGrant().Card(ctx, contract.HostexecGrantArgs{Room: room, To: contract.Principal{Kind: contract.PrincipalSession, Session: worker}, Hosts: []contract.HostName{"host"}, Reason: "Inspect logs"}, contract.Principal{})
	if card.Title != "Grant host access" || !strings.Contains(card.Body, "run any command on host, for 24 hours.\nReason: Inspect logs") {
		t.Fatalf("card: %+v", card)
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
 ALTER TABLE grants ADD COLUMN mode TEXT NOT NULL DEFAULT '';
 ALTER TABLE grants ADD COLUMN commands BLOB NOT NULL DEFAULT '[]';
 ALTER TABLE grants DROP COLUMN reason;
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
