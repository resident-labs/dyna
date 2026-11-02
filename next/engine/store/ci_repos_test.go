package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestRepoCIOnOffAndItsRoomsRule(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const id contract.RoomID = "room"
	if err := s.CreateRoom(ctx, id, "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, id, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoomRepo(ctx, id, "owner/other", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepoCI(ctx, "missing/repo", true, 0); !errors.Is(err, ErrRepoNotFound) {
		t.Fatalf("unknown repository: %v", err)
	}
	if got, err := s.CIRepos(ctx); err != nil || len(got) != 0 {
		t.Fatalf("a new repository is off: %+v %v", got, err)
	}
	if err := s.SetRepoCI(ctx, "owner/repo", true, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomOnly(ctx, id, []string{"boats", "pve"}); err != nil {
		t.Fatal(err)
	}
	want := []CIRepo{{Repo: "owner/repo", Max: 3, Room: id, Only: []string{"boats", "pve"}}}
	if got, err := s.CIRepos(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := s.SetRepoCI(ctx, "owner/repo", false, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CIRepos(ctx); err != nil || len(got) != 0 {
		t.Fatalf("after --off: %+v %v", got, err)
	}
}

// The live store had per-host counts: each repository that named hosts is on, bounded by the runners it allowed them, and
// one that named none stays off. The counts' table goes.
func TestUpgradeTurnsRepositoriesWithRunnerHostsOn(t *testing.T) {
	s, err := Open(floorStore(t, func(db *sql.DB) {
		for _, statement := range []string{
			`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('room_dyna','dyna',0,0,0),('room_horizon','horizon',0,0,0)`,
			`INSERT INTO repos(repo,default_room) VALUES('resident-labs/dyna','room_dyna'),('arismoko/horizon','room_horizon'),('arismoko/horizon-mixins','room_horizon')`,
			`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('lt-24-005','u@lt','ready',19,1,1,0),('desktop','u@desktop','ready',15,1,1,0)`,
			`INSERT INTO repo_ci_hosts(repo,host,runners,size) VALUES('resident-labs/dyna','lt-24-005',4,'medium'),('arismoko/horizon','lt-24-005',2,'large'),('arismoko/horizon','desktop',1,'large')`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.CIRepos(context.Background())
	want := []CIRepo{{Repo: "arismoko/horizon", Max: 3, Room: "room_horizon", Only: []string{}}, {Repo: "resident-labs/dyna", Max: 4, Room: "room_dyna", Only: []string{}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	var tables int
	if err := s.Read().QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='repo_ci_hosts'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("repo_ci_hosts is still there: %d %v", tables, err)
	}
}
