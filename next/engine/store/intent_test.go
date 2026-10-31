package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDispatchRechecksLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	event := contract.Event{ID: "post", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "hello"}}
	_, err = s.Post(ctx, event, []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}})
	if err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	for _, query := range []string{
		"UPDATE boxes SET want='stop'",
		"UPDATE boxes SET want='run',state='stopped'",
		"UPDATE boxes SET state='ready'; UPDATE sessions SET state='closing'",
	} {
		if _, err := s.writer.Exec(query); err != nil {
			t.Fatal(err)
		}
		turn, inputs, err := s.Dispatch(ctx, "s", "turn", start, false)
		if err != nil || turn != "" || len(inputs) != 0 {
			t.Fatalf("dispatch while ineligible: %q %v %v", turn, inputs, err)
		}
	}
	if _, err := s.writer.Exec("UPDATE sessions SET state='open'"); err != nil {
		t.Fatal(err)
	}
	turn, inputs, err := s.Dispatch(ctx, "s", "turn", start, false)
	if err != nil || turn != "turn" || len(inputs) != 1 {
		t.Fatalf("eligible dispatch: %q %v %v", turn, inputs, err)
	}
}

func TestArchiveRetryDoesNotDelayDiscard(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','closing','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want,due_at) VALUES('b','s','','agent','closing','','',0,0,'close',0)`)
	if err != nil {
		t.Fatal(err)
	}
	due := time.UnixMilli(100)
	if err := s.SetArchiveRetry(ctx, "b", due); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := s.Read().QueryRowContext(ctx, "SELECT due_at FROM boxes WHERE id='b'").Scan(&got); err != nil || got != 100 {
		t.Fatalf("archive retry = %d: %v", got, err)
	}
	if err := s.CompleteArchives(ctx, "s", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArchiveRetry(ctx, "b", due); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRowContext(ctx, "SELECT due_at FROM boxes WHERE id='b'").Scan(&got); err != nil || got != 0 {
		t.Fatalf("discard delayed = %d: %v", got, err)
	}
}

func TestCloseIntentAndArchiveCompletion(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO repos(repo,default_room) VALUES('repo','r');
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('p','r','p',NULL,'p','open','',0),('c','r','c','p','p','open','',0);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('bp','p','','agent','closing','','',0,0),('bc','c','','agent','closing','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkClosing(ctx, "p", false); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	a := Archive{Session: "c", Repo: "repo", Ref: "ref", Commit: "sha", CreatedAt: now, ExpiresAt: now}
	if err := s.CompleteArchives(ctx, "c", []Archive{a}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkClosing(ctx, "p", false); err != nil {
		t.Fatal(err)
	}
	var pw, cw, ps, cs, sha string
	err = s.Read().QueryRowContext(ctx, `SELECT p.want,c.want,ps.state,cs.state,a.commit_sha FROM boxes p JOIN boxes c ON c.id='bc' JOIN sessions ps ON ps.id='p' JOIN sessions cs ON cs.id='c' JOIN archives a ON a.session='c' WHERE p.id='bp'`).Scan(&pw, &cw, &ps, &cs, &sha)
	if err != nil || pw != "close" || cw != "discard" || ps != "closing" || cs != "closing" || sha != "sha" {
		t.Fatalf("intent: %s %s %s %s %s: %v", pw, cw, ps, cs, sha, err)
	}
	if err := s.CompleteArchives(ctx, "p", nil); err != nil {
		t.Fatal(err)
	}
}
