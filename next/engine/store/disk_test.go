package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// diskStore has two rooms with one stopped root each, a worker, a closed root, and a host named h.
func diskStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r1','r1',0,0,0),('r2','r2',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root1','r1','root',NULL,'p','open','',0),('root2','r2','root',NULL,'p','open','',0),('worker','r1','worker','root1','p','open','',0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,closed_at) VALUES('gone','r2','gone',NULL,'p','closed','',0,1);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b1','root1','','agent','stopped','','',0,0,'stop'),('b2','root2','','agent','stopped','','',0,0,'stop');
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',1,1,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// diskAlerts returns the host.disk_low events each room holds and how many waking deliveries each root has.
func diskAlerts(t *testing.T, s *Store) (events map[contract.RoomID]int, wakes map[string]int) {
	t.Helper()
	events, wakes = make(map[contract.RoomID]int), make(map[string]int)
	for _, room := range []contract.RoomID{"r1", "r2"} {
		list, err := s.Events(context.Background(), room, 0, 100, []contract.EventType{"host.disk_low"})
		if err != nil {
			t.Fatal(err)
		}
		events[room] = len(list)
	}
	rows, err := s.Read().Query("SELECT to_id,count(*) FROM deliveries WHERE wake=1 AND state='pending' GROUP BY to_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var to string
		var n int
		if err := rows.Scan(&to, &n); err != nil {
			t.Fatal(err)
		}
		wakes[to] = n
	}
	return events, wakes
}

func TestDiskWarnWakesRoomRootsOncePerSpell(t *testing.T) {
	ctx := context.Background()
	s := diskStore(t)
	const total = 1000 * box.GiB // Floor 100 GiB, warn 180 GiB, spell ends at 198 GiB.
	for i, step := range []struct {
		free   int64
		alerts int
	}{
		{500 * box.GiB, 0},
		{180 * box.GiB, 0},
		{179 * box.GiB, 1},
		{178 * box.GiB, 1},
		{181 * box.GiB, 1},
		{179 * box.GiB, 1}, // Hovering at the warn level stays quiet.
		{197 * box.GiB, 1},
		{179 * box.GiB, 1},
		{198 * box.GiB, 1},
		{179 * box.GiB, 2},
	} {
		if err := s.RecordHostHeartbeat(ctx, "h", true, &step.free, total, time.Unix(int64(i)*3600, 0)); err != nil {
			t.Fatal(err)
		}
		events, wakes := diskAlerts(t, s)
		if events["r1"] != step.alerts || events["r2"] != step.alerts || wakes["root1"] != step.alerts || wakes["root2"] != step.alerts || wakes["worker"] != 0 || wakes["gone"] != 0 {
			t.Fatalf("step %d: events %v wakes %v, want %d each for roots only", i, events, wakes, step.alerts)
		}
	}
	list, err := s.Events(ctx, "r1", 0, 100, []contract.EventType{"host.disk_low"})
	if err != nil {
		t.Fatal(err)
	}
	low, ok := list[0].Body.(contract.HostDiskLow)
	if !ok || low.Host != "h" || low.FreeBytes != 179*box.GiB || low.FloorBytes != 100*box.GiB {
		t.Fatalf("event: %#v", list[0].Body)
	}
	var want string
	if err := s.Read().QueryRow("SELECT want FROM boxes WHERE session='root1'").Scan(&want); err != nil || want != "run" {
		t.Fatalf("root box want=%s: %v", want, err)
	}
}

func TestDiskWarnReachesRootsWhenControlPlaneIsNoHost(t *testing.T) {
	ctx := context.Background()
	s := diskStore(t)
	free := 10 * box.GiB
	for range 2 {
		if err := s.RecordHostHeartbeat(ctx, contract.ControlPlane, true, &free, 1000*box.GiB, time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	events, wakes := diskAlerts(t, s)
	if events["r1"] != 1 || events["r2"] != 1 || wakes["root1"] != 1 || wakes["root2"] != 1 {
		t.Fatalf("events %v wakes %v", events, wakes)
	}
	hosts, err := s.Hosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Name != "h" {
		t.Fatalf("control plane became a host: %+v, %v", hosts, err)
	}
}

func TestHeartbeatRecordsFloorFromDiskSize(t *testing.T) {
	ctx := context.Background()
	s := diskStore(t)
	floor := func() int64 {
		t.Helper()
		hosts, err := s.Hosts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return hosts[0].DiskFloor
	}
	if got := floor(); got != 20*box.GiB {
		t.Fatalf("floor before a sample: %d", got)
	}
	free := 500 * box.GiB
	if err := s.RecordHostHeartbeat(ctx, "h", true, &free, 1000*box.GiB, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if got := floor(); got != 100*box.GiB {
		t.Fatalf("floor after a sample: %d", got)
	}
	if err := s.RecordHostHeartbeat(ctx, "h", true, nil, 0, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if got := floor(); got != 100*box.GiB {
		t.Fatalf("a failed disk probe moved the floor: %d", got)
	}
}

// The btrfs metadata rule moves free space to zero and back, past any free-space hysteresis, so only time can hold the alert.
func TestDiskAlertHoldsThroughMetadataFlap(t *testing.T) {
	ctx := context.Background()
	s := diskStore(t)
	const total = 1000 * box.GiB
	alerts := func() int {
		t.Helper()
		events, _ := diskAlerts(t, s)
		return events["r1"]
	}
	at := time.Unix(1000, 0)
	for range 180 {
		free := int64(0)
		if at.Unix()%20 == 0 {
			free = 500 * box.GiB
		}
		if err := s.RecordHostHeartbeat(ctx, "h", true, &free, total, at); err != nil {
			t.Fatal(err)
		}
		at = at.Add(10 * time.Second)
	}
	if got := alerts(); got != 1 {
		t.Fatalf("half an hour of flapping sent %d alerts", got)
	}
	for range 2 {
		free := 500 * box.GiB
		at = at.Add(time.Hour)
		if err := s.RecordHostHeartbeat(ctx, "h", true, &free, total, at); err != nil {
			t.Fatal(err)
		}
	}
	free := int64(0)
	if err := s.RecordHostHeartbeat(ctx, "h", true, &free, total, at); err != nil {
		t.Fatal(err)
	}
	if got := alerts(); got != 2 {
		t.Fatalf("a new spell after the gap: %d alerts", got)
	}
}

func TestDiskLowSinceNeedsAnUnbrokenRun(t *testing.T) {
	ctx := context.Background()
	s := diskStore(t)
	const total = 1000 * box.GiB
	low, fine := 50*box.GiB, 500*box.GiB
	since := func() (at int64) {
		t.Helper()
		if err := s.Read().QueryRow("SELECT disk_low_since FROM hosts WHERE name='h'").Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	sample := func(reachable bool, free *int64, sec int64) {
		t.Helper()
		if err := s.RecordHostHeartbeat(ctx, "h", reachable, free, total, time.Unix(sec, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for name, breakRun := range map[string]func(){
		"good sample":  func() { sample(true, &fine, 30) },
		"failed probe": func() { sample(true, nil, 30) },
		"miss":         func() { sample(false, nil, 30) },
		"restart": func() {
			if err := s.ResetEngineHosts(ctx); err != nil {
				t.Fatal(err)
			}
		},
	} {
		sample(true, &low, 10)
		sample(true, &low, 20)
		if since() != 10000 {
			t.Fatalf("%s: run started at %d", name, since())
		}
		breakRun()
		if since() != 0 {
			t.Fatalf("%s left the run standing at %d", name, since())
		}
	}
}
