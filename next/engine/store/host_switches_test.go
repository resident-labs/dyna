package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func hostSwitches(t *testing.T, s *Store, name contract.HostName) box.Host {
	t.Helper()
	hosts, err := s.Hosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("no host %s in %+v", name, hosts)
	return box.Host{}
}

// A host is added with its switches, and each turns on its own; a nil switch stays as it is.
func TestHostSwitchesTurnEachOnItsOwn(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddHost(ctx, box.Host{Name: "plain"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHost(ctx, box.Host{Name: "ci", NoAgents: true, CI: true}); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); h.NoAgents || h.CI {
		t.Fatalf("defaults: %+v", h)
	}
	if h := hostSwitches(t, s, "ci"); !h.NoAgents || !h.CI {
		t.Fatalf("as added: %+v", h)
	}
	on, off := true, false
	if err := s.SetHostSwitches(ctx, "plain", nil, &on, false); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); h.NoAgents || !h.CI {
		t.Fatalf("ci alone on: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "plain", &off, nil, false); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); !h.NoAgents || !h.CI {
		t.Fatalf("agents alone off: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "plain", &off, &off, false); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); !h.NoAgents || h.CI {
		t.Fatalf("both off: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "ci", &on, nil, false); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "ci"); h.NoAgents || !h.CI {
		t.Fatalf("agents back on: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "missing", &on, nil, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown host: %v", err)
	}
}

// Both switches off disables the host and keeps it registered, with its room list as it was.
func TestBothSwitchesOffKeepsTheHost(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHost(ctx, box.Host{Name: "h", Rooms: []contract.RoomID{"room"}, CI: true}); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := s.SetHostSwitches(ctx, "h", &off, &off, false); err != nil {
		t.Fatal(err)
	}
	h := hostSwitches(t, s, "h")
	if !h.NoAgents || h.CI || len(h.Rooms) != 1 || h.Rooms[0] != "room" {
		t.Fatalf("%+v", h)
	}
	if h.Serves("room") {
		t.Fatal("a host with agents off serves its room")
	}
}

// The upgrade keeps CI on for a host a repository already runs CI on, and agents on for every host.
func TestUpgradeKeepsCIOnWhereARepositoryRunsIt(t *testing.T) {
	s, err := Open(floorStore(t, func(db *sql.DB) {
		for _, statement := range []string{
			"INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES ('room','room',1,0,0)",
			"INSERT INTO repos(repo,default_room) VALUES ('owner/repo','room')",
			"INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES ('pve','b@pve','ready',4,1,1,1),('laptop','a@laptop','ready',4,1,1,1)",
			"INSERT INTO repo_ci_hosts(repo,host,runners,size) VALUES ('owner/repo','pve',4,'large')",
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
	if h := hostSwitches(t, s, "pve"); h.NoAgents || !h.CI {
		t.Fatalf("pve: %+v", h)
	}
	if h := hostSwitches(t, s, "laptop"); h.NoAgents || h.CI {
		t.Fatalf("laptop: %+v", h)
	}
}
