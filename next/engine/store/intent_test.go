package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDispatchRechecksLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
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
		turn, inputs, _, err := s.Dispatch(ctx, "s", "turn", start, false)
		if err != nil || turn != "" || len(inputs) != 0 {
			t.Fatalf("dispatch while ineligible: %q %v %v", turn, inputs, err)
		}
	}
	if _, err := s.writer.Exec("UPDATE sessions SET state='open'"); err != nil {
		t.Fatal(err)
	}
	s.DrainDispatch()
	turn, inputs, _, err := s.Dispatch(ctx, "s", "turn", start, false)
	if err != nil || turn != "" || len(inputs) != 0 {
		t.Fatalf("dispatch during drain: %q %v %v", turn, inputs, err)
	}
	// A new engine opens a new store and may dispatch the pending input.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	turn, inputs, _, err = s.Dispatch(ctx, "s", "turn", start, false)
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
	event := contract.Event{ID: "retry", Room: "r", Time: due, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", From: contract.BoxClosing, To: contract.BoxClosing, Reason: "failure"}}
	if err := s.SetArchiveRetry(ctx, "b", WantClose, due, "failure", event); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := s.Read().QueryRowContext(ctx, "SELECT due_at FROM boxes WHERE id='b'").Scan(&got); err != nil || got != 100 {
		t.Fatalf("archive retry = %d: %v", got, err)
	}
	if err := s.CompleteArchives(ctx, "s", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArchiveRetry(ctx, "b", WantClose, due, "failure", event); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRowContext(ctx, "SELECT due_at FROM boxes WHERE id='b'").Scan(&got); err != nil || got != 0 {
		t.Fatalf("discard delayed = %d: %v", got, err)
	}
	event.ID = "remove"
	event.Body = contract.BoxChanged{Box: "b", Session: "s", From: contract.BoxClosing, To: contract.BoxClosing, Reason: "remove failed"}
	if err := s.SetArchiveRetry(ctx, "b", WantDiscard, due, "remove failed", event); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArchiveRetry(ctx, "b", WantDiscard, due.Add(time.Millisecond), "remove failed", event); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := s.Read().QueryRowContext(ctx, "SELECT due_at,reason FROM boxes WHERE id='b'").Scan(&got, &reason); err != nil || got != 101 || reason != "remove failed" {
		t.Fatalf("retry %d %q: %v", got, reason, err)
	}
	var count int
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type='box.changed'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("events %d: %v", count, err)
	}
}

func TestLifecycleBoxesSkipsCompletedClosures(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at,closed_at) VALUES('closed','r','closed','p','closed','',0,1),('closing','r','closing','p','closing','',0,NULL);
INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want) VALUES('old','closed','','agent','deleted','','',0,0,'discard'),('pending','closing','','agent','deleted','','',0,0,'discard')`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.LifecycleBoxes(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != "pending" {
		t.Fatalf("lifecycle boxes %+v: %v", rows, err)
	}
	completed, err := s.Box(ctx, "old")
	if err != nil || completed.State != contract.BoxDeleted {
		t.Fatalf("completed box %+v: %v", completed, err)
	}
	all, err := s.Boxes(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all boxes %+v: %v", all, err)
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
	if err := s.MarkClosing(ctx, "p", false, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	a := Archive{Session: "c", Repo: "repo", Ref: "ref", Commit: "sha", CreatedAt: now, ExpiresAt: now}
	if err := s.CompleteArchives(ctx, "c", []Archive{a}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkClosing(ctx, "p", false, time.Unix(0, 0)); err != nil {
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
