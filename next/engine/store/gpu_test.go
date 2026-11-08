package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestHostKeepsItsGPUs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.writer.Exec(`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','ssh','ready',8,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	cards := []contract.GPU{{Index: 0, Name: "RTX 3090 Ti", MemBytes: 24 << 30}, {Index: 1, Name: "GTX 1050 Ti", MemBytes: 4 << 30}}
	for _, want := range [][]contract.GPU{cards, nil} {
		if err := s.SetHostGPUs(ctx, "h", want); err != nil {
			t.Fatal(err)
		}
		hosts, err := s.Hosts(ctx)
		if err != nil || len(hosts) != 1 || len(hosts[0].GPUs) != len(want) || len(want) > 0 && !reflect.DeepEqual(hosts[0].GPUs, want) {
			t.Fatalf("after setting %v: %+v, %v", want, hosts, err)
		}
	}
}

func TestBoxKeepsItsGPURequestAndCard(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','ssh','ready',8,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(10, 0)
	event := contract.Event{ID: "e", Room: "r", Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEvent := contract.Event{ID: "be", Room: "r", Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := s.StartSession(ctx, Session{ID: "s", Room: "r", Name: "s", Profile: "p", Box: "b", GPU: 4}, "", contract.BoxSizeAgent, "", event, boxEvent, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}
	boxes, err := s.Boxes(ctx)
	if err != nil || len(boxes) != 1 || boxes[0].GPU != 4 {
		t.Fatalf("started with 4 GiB: %+v, %v", boxes, err)
	}
	next := boxes[0]
	next.State, next.Host, next.GPUCard = contract.BoxCreating, "h", 1
	boxEvent.ID, boxEvent.Time = "be2", at.Add(time.Second)
	if changed, err := s.SetBoxLifecycle(ctx, boxes[0], next, time.Time{}, 0, boxEvent); err != nil || !changed {
		t.Fatalf("place: %v %v", changed, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.Box(ctx, "b")
	if err != nil || got.Host != "h" || got.GPU != 4 || got.GPUCard != 1 {
		t.Fatalf("after a restart: %+v, %v", got, err)
	}
}
