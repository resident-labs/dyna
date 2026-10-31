package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestAccountLimitsRollback(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','room',0,1,1)"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO accounts(id,proxy_id,provider,label) VALUES('a','p','claude','test')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountRooms(ctx, "a", []contract.RoomID{"r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountRooms(ctx, "a", []contract.RoomID{"missing"}); err == nil {
		t.Fatal("unknown room accepted")
	}
	var room string
	if err := s.read.QueryRowContext(ctx, "SELECT room FROM account_rooms WHERE account='a'").Scan(&room); err != nil || room != "r" {
		t.Fatalf("allowlist was not preserved: %q %v", room, err)
	}
}
