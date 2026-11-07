package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestPostReceiptsDerivedWait(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000, 0)
	if err = s.CreateRoom(ctx, "r", "r", now); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','builder','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0);
 INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',0,980000,'done','',0,0,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.Post(ctx, contract.Event{ID: "p", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Home: contract.Conversation{Kind: contract.ConversationFeed}, Text: "hello"}}, []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Wake: true}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string, wait bool) {
		t.Helper()
		rs, err := s.PostReceipts(ctx, seq, "", now)
		if err != nil || len(rs) != 1 {
			t.Fatalf("%+v %v", rs, err)
		}
		if rs[0].State != want || (rs[0].BatchDeadline != nil) != wait {
			t.Fatalf("%+v", rs[0])
		}
		if wait && !rs[0].BatchDeadline.Equal(now.Add(40*time.Second)) {
			t.Fatal(rs[0])
		}
	}
	check("pending", true)
	for _, q := range []string{"UPDATE deliveries SET wake=0", "UPDATE deliveries SET wake=1,immediate=1", "UPDATE deliveries SET immediate=0; UPDATE boxes SET want='stop'", "UPDATE boxes SET want='run',state='offline'"} {
		if _, err = s.writer.Exec(q); err != nil {
			t.Fatal(err)
		}
		check("pending", false)
	}
	if _, err = s.writer.Exec("UPDATE boxes SET state='ready'"); err != nil {
		t.Fatal(err)
	}
	rs, err := s.PostReceipts(ctx, seq, "", now.Add(time.Minute))
	if err != nil || rs[0].BatchDeadline != nil {
		t.Fatalf("expired %+v %v", rs, err)
	}
	for _, state := range []string{"taken", "read"} {
		if _, err = s.writer.Exec("UPDATE deliveries SET state=?", state); err != nil {
			t.Fatal(err)
		}
		check(state, false)
	}
}
