package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

func TestReconcilerRetriesLeadWhenAnotherBoxIsDeleted(t *testing.T) {
	s, room, first, ctx := setup(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=5")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	second, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "second", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	b, err := s.store.Box(ctx, second.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxPending || b.Reason != "cpu" {
		t.Fatalf("second lead: state=%s reason=%q, want pending cpu", b.State, b.Reason)
	}
	if _, problem := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: first, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	b, err = s.store.Box(ctx, second.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxStarting {
		t.Fatalf("second lead after deletion: state=%s, want starting", b.State)
	}
}

func TestReconcilerRetriesChildWhenDiskFrees(t *testing.T) {
	s, room, parent, ctx := setup(t)
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET disk_bytes=?", int64(20<<30))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	child, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	b, err := s.store.Box(ctx, child.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxPending || b.Reason != "disk" {
		t.Fatalf("child: state=%s reason=%q, want pending disk", b.State, b.Reason)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET disk_bytes=?", int64(100<<30))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	b, err = s.store.Box(ctx, child.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxStarting {
		t.Fatalf("child after disk frees: state=%s", b.State)
	}
}

type callerCancelHost struct {
	fakeBoxes
	entered chan struct{}
	release chan struct{}
}

func (h *callerCancelHost) Create(ctx context.Context, _ lifecycle.BoxSpec) (string, error) {
	close(h.entered)
	<-h.release
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "image", nil
}

func TestReconcilerSurvivesCancelledStartCaller(t *testing.T) {
	s, room, _, ctx := setup(t)
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	host := &callerCancelHost{entered: make(chan struct{}), release: make(chan struct{})}
	s.boxes = host
	r := lifecycle.New(s.store, host, s, nil, s.event, s.now)
	callCtx, cancel := context.WithCancel(ctx)
	type result struct {
		response contract.SessionStartResponse
		problem  *contract.Error
	}
	returned := make(chan result, 1)
	go func() {
		response, problem := s.SessionStart(callCtx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "cancelled", Profile: "test"})
		returned <- result{response, problem}
	}()
	var got result
	select {
	case got = <-returned:
		cancel()
		close(host.release)
	case <-host.entered:
		cancel()
		close(host.release)
		got = <-returned
	}
	if got.problem != nil {
		t.Fatal(got.problem)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := s.store.Box(ctx, got.response.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxStarting {
		t.Fatalf("cancelled caller: box state=%s", b.State)
	}
}

type earlyHelloHost struct {
	fakeBoxes
	service *Service
	problem *contract.Error
	err     error
}

func (h *earlyHelloHost) Create(ctx context.Context, spec lifecycle.BoxSpec) (string, error) {
	_, h.problem = h.service.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: spec.Session}, contract.BoxHelloRequest{})
	return "image", h.err
}

func TestHelloBeforeCreateReturns(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			s, room, _, ctx := setup(t)
			host := &earlyHelloHost{service: s}
			if fail {
				host.err = errors.New("create failed")
			}
			s.boxes = host
			resetLifecycle(s)
			started, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "early", Profile: "test"})
			if problem != nil {
				t.Fatal(problem)
			}
			passLifecycle(t, s, ctx)
			if host.problem != nil {
				t.Fatal(host.problem)
			}
			b, err := s.store.Box(ctx, started.Box)
			if err != nil {
				t.Fatal(err)
			}
			if fail && (b.State != contract.BoxFailed || b.Reason != "create failed") {
				t.Fatalf("failed create: %+v", b)
			}
			if !fail && (b.State != contract.BoxReady || b.Image != "image") {
				t.Fatalf("successful create: %+v", b)
			}
		})
	}
}
