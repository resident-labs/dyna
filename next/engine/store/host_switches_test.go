package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

var hostSwitchEventSeq atomic.Uint64

func hostSwitchEvent(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
	return contract.Event{
		ID:   contract.EventID(fmt.Sprintf("ev_switch_%d", hostSwitchEventSeq.Add(1))),
		Time: time.Unix(1, 0), Room: room,
		From: contract.Principal{Kind: contract.PrincipalOwner, Device: "device"},
		Body: body,
	}, nil
}

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
	if err := s.SetHostSwitches(ctx, "plain", nil, &on, nil, false, hostSwitchEvent); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); h.NoAgents || !h.CI {
		t.Fatalf("ci alone on: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "plain", &off, nil, nil, false, hostSwitchEvent); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); !h.NoAgents || !h.CI {
		t.Fatalf("agents alone off: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "plain", &off, &off, nil, false, hostSwitchEvent); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "plain"); !h.NoAgents || h.CI {
		t.Fatalf("both off: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "ci", &on, nil, nil, false, hostSwitchEvent); err != nil {
		t.Fatal(err)
	}
	if h := hostSwitches(t, s, "ci"); h.NoAgents || !h.CI {
		t.Fatalf("agents back on: %+v", h)
	}
	if err := s.SetHostSwitches(ctx, "missing", &on, nil, nil, false, hostSwitchEvent); !errors.Is(err, ErrNotFound) {
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
	if err := s.SetHostSwitches(ctx, "h", &off, &off, nil, false, hostSwitchEvent); err != nil {
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

// A later room's failed audit rolls back the switch update, create-problem clear and earlier room's event.
func TestHostSwitchAuditCommitsWithTheUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, room := range []contract.RoomID{"r1", "r2"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddHost(ctx, box.Host{Name: "h", SSH: "secret@target"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("UPDATE hosts SET create_failures=3,create_problem='create_failed' WHERE name='h'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`CREATE TRIGGER reject_switch_audit BEFORE INSERT ON events
		WHEN NEW.type='host.switches_changed' AND NEW.room='r2'
		BEGIN SELECT RAISE(ABORT, 'audit refused'); END`); err != nil {
		t.Fatal(err)
	}
	live, cancel := s.Subscribe()
	defer cancel()
	on, off := true, false
	if err := s.SetHostSwitches(ctx, "h", &off, &on, nil, true, hostSwitchEvent); err == nil {
		t.Fatal("switches committed despite a refused audit")
	}
	var agents, ci bool
	var failures, generation int
	var problem string
	read := func() {
		t.Helper()
		if err := s.Read().QueryRow("SELECT agents,ci,create_failures,create_problem,create_generation FROM hosts WHERE name='h'").Scan(&agents, &ci, &failures, &problem, &generation); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if !agents || ci || failures != 3 || problem != "create_failed" || generation != 0 {
		t.Fatalf("rolled back state: %v %v %d %q %d", agents, ci, failures, problem, generation)
	}
	for _, room := range []contract.RoomID{"r1", "r2"} {
		events, err := s.Events(ctx, room, 0, 100, nil)
		if err != nil || len(events) != 0 {
			t.Fatalf("rolled back audit in %s: %+v %v", room, events, err)
		}
	}
	select {
	case event := <-live:
		t.Fatalf("published an uncommitted audit: %+v", event)
	default:
	}
	if _, err := s.writer.Exec("DROP TRIGGER reject_switch_audit"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostSwitches(ctx, "h", &off, &on, nil, true, hostSwitchEvent); err != nil {
		t.Fatal(err)
	}
	read()
	if agents || !ci || failures != 0 || problem != "" || generation != 1 {
		t.Fatalf("committed state: %v %v %d %q %d", agents, ci, failures, problem, generation)
	}
	for _, room := range []contract.RoomID{"r1", "r2"} {
		select {
		case event := <-live:
			body, ok := event.Body.(contract.HostSwitchesChanged)
			if !ok || body.Host != "h" || body.Agents == nil || *body.Agents || body.CI == nil || !*body.CI || event.Room != room || event.From != (contract.Principal{Kind: contract.PrincipalOwner, Device: "device"}) {
				t.Fatalf("committed audit: %+v", event)
			}
		default:
			t.Fatal("committed audit not published")
		}
	}
	// Serialized concurrent retries see the stored values and emit no duplicate audit.
	results := make(chan error, 8)
	for range 8 {
		go func() { results <- s.SetHostSwitches(ctx, "h", &off, &on, nil, false, hostSwitchEvent) }()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case event := <-live:
		t.Fatalf("duplicate audit: %+v", event)
	default:
	}
	for _, table := range []string{"events", "deliveries", "operations"} {
		var count int
		if err := s.Read().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if table == "events" {
			want = 2
		}
		if count != want {
			t.Fatalf("%s count %d, want %d", table, count, want)
		}
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
