package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// A service's turn never ends, so its box is never stopped for being quiet. An update may restart it only while no handler
// holds a row, and it starts again after.
func TestServiceBoxNeverIdlesAndUpdatesWhenNoHandlerIsInFlight(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s1','r','s1','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b1','s1','','agent','ready','','',0,0);
INSERT INTO turns(id,session,started_at,started_seq,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,shell_jobs) VALUES('t1','s1',1,0,'','','0','0','0','0',0);
INSERT INTO subscriptions(id,scope,room,every_ms,wake_kind,wake_id,run_image,run_size,run_host,created_at) VALUES('sub','room','r',0,'','','','','',0);
INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at,sub,served) VALUES('wf','r','s1','svc','','','','[]','','','','running',1,'sub','{"since":"2026-01-01T00:00:00Z"}')`)
	if err != nil {
		t.Fatal(err)
	}
	sessions := []contract.SessionID{"s1"}
	if stopped, err := s.StopQuiet(ctx, sessions, time.Now()); err != nil || len(stopped) != 0 {
		t.Fatalf("a serving box idled out: %v %v", stopped, err)
	}
	if _, err := s.writer.Exec(`INSERT INTO workflow_queue(sub,key,seq,body,run,boot) VALUES('sub','k',1,'{}','wf','boot')`); err != nil {
		t.Fatal(err)
	}
	if stopped, err := s.StopStale(ctx, sessions); err != nil || len(stopped) != 0 {
		t.Fatalf("an update stopped a box with a handler in flight: %v %v", stopped, err)
	}
	if _, err := s.writer.Exec(`DELETE FROM workflow_queue`); err != nil {
		t.Fatal(err)
	}
	if stopped, err := s.StopStale(ctx, sessions); err != nil || len(stopped) != 1 {
		t.Fatalf("an update waited for no handler: %v %v", stopped, err)
	}
	if _, err := s.writer.Exec(`UPDATE boxes SET state='stopped'`); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeStopped(ctx); err != nil {
		t.Fatal(err)
	}
	var want string
	if err := s.Read().QueryRow(`SELECT want FROM boxes`).Scan(&want); err != nil || want != "run" {
		t.Fatalf("the service's box stayed stopped: %q %v", want, err)
	}
}
