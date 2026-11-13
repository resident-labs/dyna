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
	if err := s.SetAccountRooms(ctx, "a", []contract.RoomID{"r"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountRooms(ctx, "a", []contract.RoomID{"missing"}, nil); err == nil {
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

// A room's preference is a subset of the rooms the account serves, is replaced with the
// allowlist in one write, and goes with the room.
func TestPreferredAccountProxyIDs(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','room',0,1,1),('other','other',0,1,1)`,
			`INSERT INTO accounts(id,proxy_id,provider,label) VALUES('a','one','claude','one'),('b','two','claude','two')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	preferred := func(room contract.RoomID) string {
		got, err := s.PreferredAccountProxyIDs(ctx, room)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ",")
	}
	if err := s.SetAccountRooms(ctx, "a", nil, []contract.RoomID{"r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountRooms(ctx, "b", []contract.RoomID{"r", "other"}, []contract.RoomID{"r", "other"}); err != nil {
		t.Fatal(err)
	}
	if got := preferred("r"); got != "one,two" {
		t.Fatalf("r prefers %q", got)
	}
	if err := s.SetAccountRooms(ctx, "b", []contract.RoomID{"other"}, []contract.RoomID{"r"}); err != ErrPreferUnserved {
		t.Fatalf("preference outside the allowlist: %v", err)
	}
	if err := s.SetAccountRooms(ctx, "b", []contract.RoomID{"other"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, other := preferred("r"), preferred("other"); got != "one" || other != "" {
		t.Fatalf("narrowing kept preferences: %q %q", got, other)
	}
	bindings, err := s.Accounts(ctx)
	if err != nil || len(bindings) != 2 || string(bindings[0].Account.Preferred[0]) != "r" || len(bindings[0].Account.Rooms) != 0 || len(bindings[1].Account.Preferred) != 0 || string(bindings[1].Account.Rooms[0]) != "other" {
		t.Fatalf("accounts: %+v %v", bindings, err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM rooms WHERE id='r'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := preferred("r"); got != "" {
		t.Fatalf("a deleted room keeps %q", got)
	}
}
