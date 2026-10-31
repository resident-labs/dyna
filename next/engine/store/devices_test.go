package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDeviceExpiryRenewalAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(100000, 0)
	d := Device{ID: "one", Name: "browser", TokenHash: [32]byte{1}, PairedAt: now, LastSeenAt: now, ExpiresAt: now.Add(DeviceLife), Scopes: []contract.Scope{contract.ScopeRead}}
	if err := s.CreateDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	check := func(at time.Time, want bool) {
		t.Helper()
		_, ok, err := s.AuthenticateDevice(ctx, d.TokenHash, at)
		if err != nil || ok != want {
			t.Fatalf("auth at %s: %v %v", at, ok, err)
		}
	}
	check(now.Add(23*time.Hour), true)
	var seen int64
	if err := s.read.QueryRow("SELECT last_seen_at FROM devices WHERE id='one'").Scan(&seen); err != nil || seen != now.UnixMilli() {
		t.Fatalf("early write: %d %v", seen, err)
	}
	check(now.Add(24*time.Hour), true)
	if err := s.read.QueryRow("SELECT last_seen_at FROM devices WHERE id='one'").Scan(&seen); err != nil || seen != now.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("renewal: %d %v", seen, err)
	}
	check(now.Add(DeviceLife), true)
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE devices SET revoked_at=? WHERE id='one'", now.UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(now.Add(DeviceLife), false)
	d.ID = "two"
	d.TokenHash = [32]byte{2}
	if err := s.CreateDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	_, ok, err := s.AuthenticateDevice(ctx, d.TokenHash, now.Add(DeviceLife))
	if err != nil || ok {
		t.Fatalf("expired: %v %v", ok, err)
	}
}
