package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestFirstModelTimestamp(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0),('other','r','other','p','open','',0);
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',1,'','',0,0,0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, step := range []struct {
		session contract.SessionID
		model   int64
		want    int64
	}{{"other", 100, 0}, {"s", 0, 0}, {"s", 123, 123}, {"s", 456, 123}, {"s", 123, 123}, {"s", 0, 123}} {
		if step.model != 0 {
			if err := s.MarkTurnModelSent(ctx, step.session, time.UnixMilli(step.model)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.MarkTurnActive(ctx, "s", "t", time.Now()); err != nil {
			t.Fatal(err)
		}
		var got int64
		if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='t'").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != step.want {
			t.Fatalf("model_sent_at=%d want %d", got, step.want)
		}
	}
}
