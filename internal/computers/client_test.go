package computers

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := filepath.Join(t.TempDir(), "control.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeSupervisor("test")
	go fake.Serve(ctx, l)
	c := &Client{Socket: socket}
	items, err := c.List(ctx, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	s, err := c.Status(ctx)
	if err != nil || s.State != StateCreated {
		t.Fatalf("status: %+v %v", s, err)
	}
	v, err := c.OpenView(ctx)
	if err != nil || v.ViewerID == "" {
		t.Fatal(v, err)
	}
	if _, err = c.SetControl(ctx, ControlHuman, v.ViewerID); err != nil {
		t.Fatal(err)
	}
	s, _ = c.Status(ctx)
	if s.Viewers != 1 || s.Control != ControlHuman {
		t.Fatal(s)
	}
	defer v.Close()
	if _, err = c.CloseView(ctx, v.ViewerID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Act(ctx, []Action{}, time.Second, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = c.SetControl(ctx, "bad", v.ViewerID); err == nil {
		t.Fatal("accepted invalid control")
	}
	if _, err = c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidID(t *testing.T) {
	for _, id := range []string{"", "../owner", "x/y"} {
		if ValidateID(id) == nil {
			t.Fatal(id)
		}
	}
}

func TestViewerLeaseReleasesOnConnectionLoss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeSupervisor("lease")
	fake.Changed = make(chan struct{}, 4)
	go fake.Serve(ctx, listener)
	c := &Client{Socket: socket, Session: "lease"}
	lease, err := c.OpenView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lease.SetControl(ctx, ControlHuman); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	// Every viewer change reports one token, so the release is an event.
	for {
		select {
		case <-fake.Changed:
		case <-time.After(2 * time.Second):
			t.Fatal("viewer disconnect did not release human control")
		}
		s, err := c.Status(ctx)
		if err == nil && s.Viewers == 0 && s.Control == ControlAgent {
			return
		}
	}
}
