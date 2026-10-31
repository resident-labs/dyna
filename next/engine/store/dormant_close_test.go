package store

import (
	"context"
	"testing"
	"time"
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
		{"child", "INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('c','r','c','s','p','open','',0)", false},
		{"pr", "INSERT INTO repos(repo,default_room) VALUES('repo','r'); INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('repo',1,'s','','')", false},
		{"delivery", `INSERT INTO events(id,room,at,from_kind,from_id,type,body) VALUES('e','r',0,'engine','','post.created',x'7b7d'); INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state) VALUES('session','s',last_insert_rowid(),'session','none','','','','',0,'pending')`, false},
		{"not stopped", "UPDATE boxes SET state='ready' WHERE session='s'", false},
		{"operation", "INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('o','x','r','session','s','owner','pending',x'00',x'00',x'00',0,1)", false},
		{"shell", "UPDATE sessions SET shell_jobs=1 WHERE id='s'", false},
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
				changed, err := s.MarkDormantClosing(ctx, "s", now.Add(-7*24*time.Hour))
				if err != nil || changed {
					t.Fatalf("late work: %v %v", changed, err)
				}
				if _, err := s.writer.Exec("DELETE FROM operations WHERE id='late'"); err != nil {
					t.Fatal(err)
				}
			}
			changed, err := s.MarkDormantClosing(ctx, "s", now.Add(-7*24*time.Hour))
			if err != nil || changed != tc.eligible {
				t.Fatalf("mark %v: %v", changed, err)
			}
		})
	}
}
