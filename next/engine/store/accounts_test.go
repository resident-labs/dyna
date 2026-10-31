package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"strings"
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

func TestEligibleAccountProxyIDs(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','room',0,1,1),('other','other',0,1,1)`,
			`INSERT INTO accounts(id,proxy_id,provider,label) VALUES('a','one','claude','one'),('b','two','claude','two')`,
			`INSERT INTO account_rooms(account,room) VALUES('b','r')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		room contract.RoomID
		want string
	}{{"r", "one,two"}, {"other", "one"}, {"unknown", "one"}} {
		got, err := s.EligibleAccountProxyIDs(ctx, tc.room)
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Fatalf("%s: %v %v", tc.room, got, err)
		}
	}
}
