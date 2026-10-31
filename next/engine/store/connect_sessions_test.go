package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestConnectDeviceMappingAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(10000, 0)
	if err := s.InitializeConnectLink(ctx, 1); err != nil {
		t.Fatal(err)
	}
	x := ConnectSession{ID: "first", RelayDevice: "dev_example", Device: "device1", Origin: "https://test.example", JKT: "key", User: "user", Epoch: 1, Generation: 1, Hash: sha256.Sum256([]byte("first")), Created: now, Access: now.Add(time.Minute), End: now.Add(time.Hour), Grant: now}
	if err := s.StartConnectSession(ctx, x); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, e := tx.Exec("DELETE FROM device_scopes WHERE device=? AND scope<>?", x.Device, contract.ScopeRead)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	x.ID, x.Device, x.Hash = "second", "device2", sha256.Sum256([]byte("second"))
	if err := s.StartConnectSession(ctx, x); err != nil {
		t.Fatal(err)
	}
	got, scopes, ok, err := s.AuthenticateConnect(ctx, x.Hash, x.Origin, now)
	if err != nil || !ok || got.Device != "device1" || len(scopes) != 1 || scopes[0] != contract.ScopeRead {
		t.Fatalf("mapping: %+v %v %v %v", got, scopes, ok, err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, e := tx.Exec("UPDATE devices SET revoked_at=? WHERE id=?", now.UnixMilli(), got.Device)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.AuthenticateConnect(ctx, x.Hash, x.Origin, now); err != nil || ok {
		t.Fatalf("revoked auth: %v %v", ok, err)
	}
	x.ID = "third"
	if err := s.StartConnectSession(ctx, x); err == nil {
		t.Fatal("revoked mapping was reused")
	}
}
