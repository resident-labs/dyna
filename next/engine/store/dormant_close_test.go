package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	addPR        = "INSERT INTO repos(repo,default_room) VALUES('repo','r'); INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('repo',1,'s','','')"
	addChild     = "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('c','r','c','s','p','open','',0)"
	addDelivery  = `INSERT INTO events(id,room,at,from_kind,from_id,type,body) VALUES('e','r',0,'engine','','post.created',x'7b7d'); INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state) VALUES('session','s',last_insert_rowid(),'session','none','','','','',0,'pending')`
	addOperation = "INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('o','x','r','session','s','owner','pending',x'00',x'00',x'00',0,1)"
	lowFrom      = 1000 // Host h has been low since before this, in the hosts table's milliseconds.
	addTurn      = "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',0,'','m',0,0,0,0)"
)

func TestDormantCloseEligibility(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000000, 0)
	for _, tc := range []struct {
		name, change string
		eligible     bool
	}{
		{"eligible", "", true},
		{"root", "UPDATE sessions SET parent=NULL WHERE id='s'", false},
		{"paused", "UPDATE rooms SET paused_at=1", false},
		{"closing", "UPDATE sessions SET state='closing' WHERE id='s'", false},
		{"running", "UPDATE boxes SET want='run' WHERE session='s'", false},
		{"recent", "UPDATE boxes SET updated_at=999999999 WHERE session='s'", false},
		{"child", addChild, false},
		{"pr", addPR, false},
		{"delivery", addDelivery, false},
		{"not stopped", "UPDATE boxes SET state='ready' WHERE session='s'", false},
		{"operation", addOperation, false},
		{"shell", "UPDATE sessions SET shell_jobs=1 WHERE id='s'", false},
		{"turn", addTurn, false},
		{"failed", "UPDATE boxes SET state='failed',want='run' WHERE session='s'", true},
		{"recent failed", "UPDATE boxes SET state='failed',updated_at=999999999 WHERE session='s'", false},
		{"failed with a delivery", "UPDATE boxes SET state='failed',want='run' WHERE session='s';" + addDelivery, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0),('s','r','s','root','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b','s','','agent','stopped','','',0,0,'stop');` + tc.change)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := s.DormantCloseCandidates(ctx, now.Add(-7*24*time.Hour))
			if err != nil || (len(ids) == 1) != tc.eligible {
				t.Fatalf("candidates %v: %v", ids, err)
			}
			if tc.eligible {
				_, err = s.writer.Exec("INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('late','x','r','session','s','owner','pending',x'00',x'00',x'00',0,1)")
				if err != nil {
					t.Fatal(err)
				}
				changed, err := s.MarkDormantClosing(ctx, "s", now.Add(-7*24*time.Hour), now)
				if err != nil || changed {
					t.Fatalf("late work: %v %v", changed, err)
				}
				if _, err := s.writer.Exec("DELETE FROM operations WHERE id='late'"); err != nil {
					t.Fatal(err)
				}
			}
			changed, err := s.MarkDormantClosing(ctx, "s", now.Add(-7*24*time.Hour), now)
			if err != nil || changed != tc.eligible {
				t.Fatalf("mark %v: %v", changed, err)
			}
		})
	}
}

func TestDormantSweepTakesTwoHundred(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var seed strings.Builder
	seed.WriteString("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0); INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0);")
	for i := range 201 {
		fmt.Fprintf(&seed, "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('s%d','r','s%d','root','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b%d','s%d','','agent','stopped','','',0,0,'stop');", i, i, i, i)
	}
	if _, err := s.writer.Exec(seed.String()); err != nil {
		t.Fatal(err)
	}
	ids, err := s.DormantCloseCandidates(context.Background(), time.Unix(1000000, 0))
	if err != nil || len(ids) != 200 {
		t.Fatalf("candidates %d: %v", len(ids), err)
	}
}

// evictStore holds a recent box that owns a PR, on host h. The host has 10 GiB free against a 20 GiB floor.
func evictStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,disk_low_since) VALUES('h','','ready',1,1,10737418240,0,1),('fine','','ready',1,1,107374182400,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0),('s','r','s','root','p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want) VALUES('rb','root','h','','agent','stopped','','',0,0,'stop'),('b','s','h','','agent','stopped','','',0,999999999,'stop');` + addPR)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDiskEvictionSkipsWaitAndPRButNotWork(t *testing.T) {
	ctx := context.Background()
	notAged, lowSince := time.Unix(0, 0), time.UnixMilli(lowFrom)
	for _, tc := range []struct {
		name, change string
		eligible     bool
	}{
		{"recent and owns a PR", "", true},
		{"failed", "UPDATE boxes SET state='failed',want='run' WHERE session='s'", true},
		{"exactly at the floor", "UPDATE hosts SET disk_bytes=disk_floor", true},
		{"one byte above the floor", "UPDATE hosts SET disk_bytes=disk_floor+1", false},
		{"reached the floor just now", "UPDATE hosts SET disk_low_since=2000", false},
		{"no run of low samples", "UPDATE hosts SET disk_low_since=0", false},
		{"host unreachable", "UPDATE hosts SET state='unreachable'", false},
		{"other host", "UPDATE boxes SET host='fine' WHERE session='s'", false},
		{"root", "UPDATE sessions SET parent=NULL WHERE id='s'", false},
		{"paused", "UPDATE rooms SET paused_at=1", false},
		{"closing", "UPDATE sessions SET state='closing' WHERE id='s'", false},
		{"wanted", "UPDATE boxes SET want='run' WHERE session='s'", false},
		{"ready", "UPDATE boxes SET state='ready' WHERE session='s'", false},
		{"child", addChild, false},
		{"delivery", addDelivery, false},
		{"operation", addOperation, false},
		{"approved operation", strings.Replace(addOperation, "'pending'", "'approved'", 1), false},
		{"running operation", strings.Replace(addOperation, "'pending'", "'running'", 1), false},
		{"finished operation", strings.Replace(addOperation, "'pending'", "'done'", 1), true},
		{"shell", "UPDATE sessions SET shell_jobs=1 WHERE id='s'", false},
		{"turn", addTurn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := evictStore(t)
			if tc.change != "" {
				if _, err := s.writer.Exec(tc.change); err != nil {
					t.Fatal(err)
				}
			}
			ids, err := s.DiskEvictCandidates(ctx, lowSince)
			if err != nil || (len(ids) == 1) != tc.eligible {
				t.Fatalf("candidates %v: %v", ids, err)
			}
			changed, err := s.MarkDormantClosing(ctx, "s", notAged, lowSince)
			if err != nil || changed != tc.eligible {
				t.Fatalf("recheck %v: %v", changed, err)
			}
			if !tc.eligible {
				return
			}
			var reason, want string
			if err := s.Read().QueryRow("SELECT close_reason,(SELECT want FROM boxes WHERE session='s') FROM sessions WHERE id='s'").Scan(&reason, &want); err != nil || reason != "disk" || want != "close" {
				t.Fatalf("reason %q want %q: %v", reason, want, err)
			}
		})
	}
}

func TestDiskEvictionRecheckSeesFreedDisk(t *testing.T) {
	ctx := context.Background()
	s := evictStore(t)
	if ids, err := s.DiskEvictCandidates(ctx, time.UnixMilli(lowFrom)); err != nil || len(ids) != 1 {
		t.Fatalf("candidates %v: %v", ids, err)
	}
	if _, err := s.writer.Exec("UPDATE hosts SET disk_bytes=disk_floor+1"); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.MarkDormantClosing(ctx, "s", time.Unix(0, 0), time.UnixMilli(lowFrom)); err != nil || changed {
		t.Fatalf("closed after disk freed: %v %v", changed, err)
	}
}

func TestDiskEvictionTakesOldestAndPaces(t *testing.T) {
	ctx := context.Background()
	s := evictStore(t)
	if _, err := s.writer.Exec("DELETE FROM pr_owners; UPDATE boxes SET updated_at=500 WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	for i, updated := range []int{400, 100, 300, 200, 600, 50} {
		host := "h"
		if updated == 50 {
			host = "fine" // The oldest box of all sits on a host with room.
		}
		_, err := s.writer.Exec(fmt.Sprintf("INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('s%d','r','s%d','root','p','open','',0); INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want) VALUES('b%d','s%d','%s','','agent','stopped','','',0,%d,'stop')", i, i, i, i, host, updated))
		if err != nil {
			t.Fatal(err)
		}
	}
	lowSince := time.UnixMilli(lowFrom)
	ids, err := s.DiskEvictCandidates(ctx, lowSince)
	if err != nil || fmt.Sprint(ids) != "[s1 s3 s2 s0]" {
		t.Fatalf("candidates %v: %v", ids, err)
	}
	// Four closes are asked for, even if no pass has moved their boxes to closing yet, so the next read waits for them.
	if _, err := s.writer.Exec("UPDATE boxes SET want='close' WHERE id IN ('b0','b1','b2','b3')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("UPDATE sessions SET state='closing' WHERE id IN ('s0','s1','s2','s3')"); err != nil {
		t.Fatal(err)
	}
	ids, err = s.DiskEvictCandidates(ctx, lowSince)
	if err != nil || len(ids) != 0 {
		t.Fatalf("before a pass moves them: %v %v", ids, err)
	}
	if _, err := s.writer.Exec("UPDATE boxes SET state='closing' WHERE id IN ('b0','b1','b2','b3')"); err != nil {
		t.Fatal(err)
	}
	ids, err = s.DiskEvictCandidates(ctx, lowSince)
	if err != nil || len(ids) != 0 {
		t.Fatalf("while four close: %v %v", ids, err)
	}
	// A close stuck on a failed archive must not hold the rest of the host.
	if _, err := s.writer.Exec("UPDATE boxes SET reason='no space' WHERE id='b0'"); err != nil {
		t.Fatal(err)
	}
	ids, err = s.DiskEvictCandidates(ctx, lowSince)
	if err != nil || fmt.Sprint(ids) != "[s s4]" {
		t.Fatalf("after one stuck close: %v %v", ids, err)
	}
}

// A failure common to every box must not walk through the host: each failed close stays running, started without admission.
func TestDiskEvictionStopsWhenClosesKeepFailing(t *testing.T) {
	ctx := context.Background()
	s := evictStore(t)
	for i := range 8 {
		_, err := s.writer.Exec(fmt.Sprintf("INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('x%d','r','x%d','root','p','closing','',0); INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,want) VALUES('xb%d','x%d','h','','agent','closing','','',0,0,'close')", i, i, i, i))
		if err != nil {
			t.Fatal(err)
		}
	}
	stuck := func(n int) {
		t.Helper()
		if _, err := s.writer.Exec(fmt.Sprintf("UPDATE boxes SET reason='' WHERE id LIKE 'xb%%'; UPDATE boxes SET reason='remote down' WHERE id IN (SELECT id FROM boxes WHERE id LIKE 'xb%%' ORDER BY id LIMIT %d)", n)); err != nil {
			t.Fatal(err)
		}
	}
	lowSince := time.UnixMilli(lowFrom)
	stuck(7) // One close still runs, so there is room for one more kind of work, and the stuck ones are under the cap.
	if ids, err := s.DiskEvictCandidates(ctx, lowSince); err != nil || len(ids) != 1 {
		t.Fatalf("under the cap: %v %v", ids, err)
	}
	stuck(8)
	if ids, err := s.DiskEvictCandidates(ctx, lowSince); err != nil || len(ids) != 0 {
		t.Fatalf("at the cap: %v %v", ids, err)
	}
}
