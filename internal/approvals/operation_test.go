package approvals

import (
	"context"
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/authority"
)

type testPayload struct{ Class Class }

func TestLogKeepsDecisionRoomsAndLegacyActions(t *testing.T) {
	f := newFixture(t)
	r := f.create(t, ClassAgent)
	db, err := f.service.db()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO approval_requests(room,id,requester,kind,class,payload,payload_hash,repository,server,command,tool,title,body,state,at,expires_at,result,created_at,updated_at) SELECT 'other',id,requester,kind,class,payload,payload_hash,repository,server,command,tool,title,body,state,at,expires_at,result,created_at,updated_at FROM approval_requests WHERE room=? AND id=?`, f.rec.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO approval_decisions(room,request,actor,action,reason,at) VALUES(?,?,?,?,?,?),(?,?,?,?,?,?)`, f.rec.ID, r.ID, f.child.ID, ActionAuto, "", f.now.UnixNano(), "other", r.ID, f.child.ID, ActionDeny, "other room", f.now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE approval_requests SET state='done' WHERE room=? AND id=?`, f.rec.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO approval_decisions(room,request,actor,action,reason,at) VALUES(?,?,?,?,?,?)`, f.rec.ID, r.ID, f.parent.ID, ActionPass, "", f.now.Add(time.Second).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	log, err := f.service.Log(f.owner(), f.rec.ID, 0)
	if err != nil || len(log) != 2 || log[0].Action != ActionPass || log[1].Action != ActionAuto || log[0].State != "" {
		t.Fatalf("legacy log = %+v, %v", log, err)
	}
}

func TestInlineExecutionErrorOverridesZeroExit(t *testing.T) {
	f := newFixture(t)
	Register(f.service, Operation[testPayload]{Kind: "test.failure", Decide: func(context.Context, authority.Actor, testPayload) (Decision, error) {
		return Decision{Class: ClassAuto}, nil
	}, Card: func(testPayload) Card { return Card{} }, Check: func(context.Context, authority.Actor, testPayload) error { return nil }, Execute: func(context.Context, authority.Actor, testPayload) (Result, error) {
		code := 0
		return Result{ExitCode: &code}, errors.New("claim failed")
	}})
	out, err := Submit(context.Background(), f.service, f.actor(f.root), "test.failure", testPayload{})
	if err != nil || out.Result == nil || out.Result.ExitCode == nil || *out.Result.ExitCode == 0 || out.Result.Reason != "claim failed" {
		t.Fatalf("failure = %+v, %v", out, err)
	}
}

func TestSubmitRoutesAndAudits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	runs := make(chan struct{}, 4)
	Register(f.service, Operation[testPayload]{
		Kind: "test.write",
		Decide: func(_ context.Context, _ authority.Actor, p testPayload) (Decision, error) {
			if p.Class == "" {
				return Decision{}, errors.New("refused")
			}
			return Decision{Class: p.Class}, nil
		},
		Card:  func(testPayload) Card { return Card{Repository: "test/repo"} },
		Check: func(context.Context, authority.Actor, testPayload) error { return nil },
		Execute: func(context.Context, authority.Actor, testPayload) (Result, error) {
			runs <- struct{}{}
			return Result{}, nil
		},
	})
	for _, tc := range []struct {
		actor authority.Actor
		class Class
	}{
		{f.actor(f.root), ClassAgent}, {f.actor(f.child), ClassAuto},
	} {
		out, err := Submit(ctx, f.service, tc.actor, "test.write", testPayload{tc.class})
		if err != nil || out.Request.ID != "" {
			t.Fatalf("inline = %+v, %v", out, err)
		}
		<-runs
	}
	msgs, err := f.rooms.Messages(f.rec.ID)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("inline messages = %+v, %v", msgs, err)
	}
	db, err := f.service.db()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM approval_requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("inline request count = %d, %v", count, err)
	}
	log, err := f.service.Log(f.owner(), f.rec.ID, 0)
	if err != nil || len(log) != 2 {
		t.Fatalf("inline log = %+v, %v", log, err)
	}
	held, err := Submit(ctx, f.service, f.actor(f.child), "test.write", testPayload{ClassAgent})
	if err != nil || held.Request.At != "parent" {
		t.Fatalf("held = %+v, %v", held, err)
	}
	if _, err := f.service.Decide(ctx, f.actor(f.parent), DecisionInput{Room: f.rec.ID, RequestID: held.Request.ID, Action: ActionApprove}); err != nil {
		t.Fatal(err)
	}
	<-runs
	f.service.inflight.Wait()
	log, err = f.service.Log(f.owner(), f.rec.ID, 0)
	if err != nil || len(log) != 3 {
		t.Fatalf("combined log = %+v, %v", log, err)
	}
	owner, err := Submit(ctx, f.service, f.actor(f.root), "test.write", testPayload{ClassOwner})
	if err != nil || owner.Request.At != Owner {
		t.Fatalf("owner = %+v, %v", owner, err)
	}
	if _, err := Submit(ctx, f.service, f.actor(f.root), "test.write", testPayload{}); err == nil {
		t.Fatal("refusal succeeded")
	}
	log, err = f.service.Log(f.owner(), f.rec.ID, 0)
	if err != nil || len(log) != 3 {
		t.Fatalf("refusal log = %+v, %v", log, err)
	}
}
