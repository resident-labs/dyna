package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestIdleWakeAvoidsWriterTransactions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	s.SetWake(func([]contract.SessionID, []contract.Event) { writes++ })
	if err := s.WakeModelRetries(ctx, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Errorf("idle retry opened %d write transactions", writes)
	}
	writes = 0
	_, _, _, err = s.Dispatch(ctx, "s", "turn", contract.Event{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("idle dispatch opened %d write transactions", writes)
	}
}

func TestStopQuietBatchesCandidates(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s1','r','s1','p','open','',0),('s2','r','s2','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b1','s1','','agent','ready','','',0,0),('b2','s2','','agent','ready','','',0,0);
INSERT INTO turns(id,session,started_at,ended_at,started_seq,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,shell_jobs) VALUES('t1','s1',1,2,0,'','','0','0','0','0',0),('t2','s2',1,2,0,'','','0','0','0','0',0)`)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	s.SetWake(func([]contract.SessionID, []contract.Event) { writes++ })
	stopped, err := s.StopQuiet(ctx, []contract.SessionID{"s1", "s2"}, time.Unix(1000, 0))
	if err != nil || len(stopped) != 2 || writes != 1 {
		t.Fatalf("stopped %v in %d transactions: %v", stopped, writes, err)
	}
}
