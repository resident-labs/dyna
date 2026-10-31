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
	pre := x
	pre.ID, pre.RelayDevice, pre.Device = "pending", "dev_pending", "device_pending"
	pre.Hash = sha256.Sum256([]byte("pending"))
	pre.Minted = now.Add(-time.Second)
	if err := s.RevokeConnectDevice(ctx, pre.RelayDevice, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.StartConnectSession(ctx, pre); err == nil {
		t.Fatal("mint before relay revocation was accepted")
	}
	pre.Minted = now.Add(61 * time.Second)
	pre.Created = pre.Minted
	if err := s.StartConnectSession(ctx, pre); err != nil {
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
	rotated := sha256.Sum256([]byte("rotated"))
	if err := s.RotateConnectSession(ctx, got, rotated, now, false); err != nil {
		t.Fatal(err)
	}
	old, _, ok, err := s.AuthenticateConnect(ctx, x.Hash, x.Origin, now.Add(31*time.Second))
	if err != nil || !ok || !old.CookieStale {
		t.Fatalf("old cookie: %+v %v %v", old, ok, err)
	}
	streamScopes, alive, err := s.ConnectStreamScopes(ctx, got.ID, now.Add(31*time.Second))
	if err != nil || !alive || len(streamScopes) != 1 || streamScopes[0] != contract.ScopeRead {
		t.Fatalf("stream: %v %v %v", streamScopes, alive, err)
	}
	x.Hash = rotated
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, e := tx.Exec("UPDATE devices SET revoked_at=? WHERE id=?", now.UnixMilli(), got.Device)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.AuthenticateConnect(ctx, x.Hash, x.Origin, now); err != nil || ok {
		t.Fatalf("revoked auth: %v %v", ok, err)
	}
	if _, alive, err := s.ConnectStreamScopes(ctx, got.ID, now); err != nil || alive {
		t.Fatalf("revoked stream: %v %v", alive, err)
	}
	x.ID = "third"
	if err := s.StartConnectSession(ctx, x); err == nil {
		t.Fatal("mint from before revocation was accepted")
	}
	x.Minted = now.Add(61 * time.Second)
	x.Created = x.Minted
	if err := s.StartConnectSession(ctx, x); err != nil {
		t.Fatal(err)
	}
	got, scopes, ok, err = s.AuthenticateConnect(ctx, x.Hash, x.Origin, x.Created)
	if err != nil || !ok || got.Device != "device2" || len(scopes) != 1 || scopes[0] != contract.ScopeRead {
		t.Fatalf("new device: %+v %v %v %v", got, scopes, ok, err)
	}
	if err := s.UnlinkConnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeConnectLink(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.StartConnectSession(ctx, x); err == nil {
		t.Fatal("old link generation was accepted")
	}
}
