package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestPinnedWaitersReserveDiskOnAdmission(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','ssh','ready',8,34359738368,32212254720,100000);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s1','r','s1','p','open','',1000),('s2','r','s2','p','open','',2000);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b1','s1','h','','small','pending','','',1000,1000),('b2','s2','h','','small','pending','','',2000,2000)`)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := s.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := box.Host{Name: "h", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 30 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: time.Unix(100, 0)}
	if p := box.Admit([]box.Host{host}, boxes, boxes[0]); p.Host != "h" {
		t.Fatal(p)
	}
	next := boxes[0]
	next.State = contract.BoxCreating
	event := contract.Event{ID: "e", Room: "r", Time: time.Unix(101, 0), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	changed, err := s.SetBoxLifecycle(ctx, boxes[0], next, time.Time{}, 0, event)
	if err != nil || !changed {
		t.Fatalf("admission: %v %v", changed, err)
	}
	// The current reconciliation pass holds the old timestamp in its snapshot.
	boxes[0] = next
	if p := box.Admit([]box.Host{host}, boxes, boxes[1]); p.Reason != box.PendingDisk {
		t.Fatalf("same pass: %+v", p)
	}
	boxes, err = s.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host.CheckedAt = time.Unix(102, 0) // The host sampled while creation was still running.
	if _, err := s.writer.ExecContext(ctx, "UPDATE boxes SET state='ready' WHERE id='b1'"); err != nil {
		t.Fatal(err)
	}
	boxes, err = s.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := box.Admit([]box.Host{host}, boxes, boxes[1]); p.Reason != box.PendingDisk {
		t.Fatalf("early hello: %+v", p)
	}
	event.ID, event.Time = "done", time.Unix(103, 0)
	if err := s.FinishCreate(ctx, boxes[0], "image", "", time.Time{}, 0, event); err != nil {
		t.Fatal(err)
	}
	boxes, err = s.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := box.Admit([]box.Host{host}, boxes, boxes[1]); p.Reason != box.PendingDisk {
		t.Fatalf("after create: %+v", p)
	}
	host.CheckedAt = time.Unix(104, 0)
	if p := box.Admit([]box.Host{host}, boxes, boxes[1]); p.Host != "h" {
		t.Fatalf("after sample: %+v", p)
	}
}
