package engine

import (
	"context"
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type wakeDuringStopHost struct {
	fakeBoxes
	entered   chan struct{}
	release   chan struct{}
	cancelled chan bool
}

func (h *wakeDuringStopHost) Stop(ctx context.Context, _ contract.BoxID) error {
	close(h.entered)
	select {
	case <-ctx.Done():
		h.cancelled <- true
	case <-h.release:
		h.cancelled <- false
	}
	return nil
}

func TestReconcilerWakeDuringStop(t *testing.T) {
	s, room, sid, ctx := setup(t)
	if err := s.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.Reconciler().Wait()
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET want='stop' WHERE id=?", session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	host := &wakeDuringStopHost{entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan bool, 1)}
	r := lifecycle.New(s.store, host, s, nil, s.event, s.now)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	<-host.entered
	_, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{
		Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: sid}, Text: "wake",
	})
	if problem != nil {
		close(host.release)
		r.Wait()
		t.Fatal(problem)
	}
	if err := r.Pass(ctx); err != nil {
		close(host.release)
		r.Wait()
		t.Fatal(err)
	}
	close(host.release)
	r.Wait()
	if <-host.cancelled {
		t.Fatal("wake cancelled the in-flight stop")
	}
	stopped, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != contract.BoxStopped {
		t.Fatalf("successful stop: state=%s, want stopped", stopped.State)
	}
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxStarting {
		t.Fatalf("wake during stop: state=%s, want starting", b.State)
	}
}

type openTerminalView struct{}

func (openTerminalView) OpenTerminal(contract.SessionID) bool { return true }
func (openTerminalView) SessionClosed(contract.SessionID)     {}
func (openTerminalView) Stale(contract.SessionID) bool        { return false }
func (openTerminalView) Connected(contract.SessionID) bool    { return false }

type stopCountingHost struct {
	fakeBoxes
	stops int
}

func (h *stopCountingHost) Stop(context.Context, contract.BoxID) error {
	h.stops++
	return nil
}

func TestOpenTerminalPreventsCommittedQuietStop(t *testing.T) {
	s, _, sid, ctx := setup(t)
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET want='stop' WHERE id=?", session.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	host := &stopCountingHost{}
	r := lifecycle.New(s.store, host, openTerminalView{}, nil, s.event, s.now)
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxReady || host.stops != 0 {
		t.Fatalf("box=%+v stops=%d err=%v", b, host.stops, err)
	}
}
