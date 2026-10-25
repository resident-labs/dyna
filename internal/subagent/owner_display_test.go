package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

// TestOwnerDisplayGrantNeedsRootOrOwner proves the owner's display is never an
// agent's to take: the owner and root may grant it to a new session, it is off
// by default, and an agent session cannot grant it, not even a session that
// already holds a grant.
func TestOwnerDisplayGrantNeedsRootOrOwner(t *testing.T) {
	e := newTestEnv(t)
	start := func(ctx context.Context, grant bool) (Info, error) {
		info, _, err := e.m.Start(ctx, StartOptions{
			Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(),
			Task: "SLEEP:0\nRESPOND: ready", OwnerDisplay: grant,
		})
		return info, err
	}
	granted := map[string]bool{}
	for _, caller := range []authority.Actor{{Role: authority.Owner}, {Role: authority.Root}} {
		info, err := start(authority.WithActor(context.Background(), caller), true)
		if err != nil {
			t.Fatalf("%s granting the display: %v", caller.Role, err)
		}
		meta, err := e.m.Store.ReadMeta(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !meta.OwnerDisplay {
			t.Fatalf("%s started %s without the stored owner display", caller.Role, info.ID)
		}
		granted[info.ID] = true
	}
	plain, err := start(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), false)
	if err != nil {
		t.Fatal(err)
	}
	if meta, err := e.m.Store.ReadMeta(plain.ID); err != nil || meta.OwnerDisplay {
		t.Fatalf("a start without the option stored owner display %v, %v", meta.OwnerDisplay, err)
	}
	if len(granted) != 2 {
		t.Fatalf("granted sessions = %v; want the owner's and root's", granted)
	}
	before, err := e.m.Store.List(true)
	if err != nil {
		t.Fatal(err)
	}
	for holder := range granted {
		_, err := start(authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: holder, Room: e.room.ID}), true)
		if err == nil || !strings.Contains(err.Error(), "owner display grant is owner or root only") {
			t.Fatalf("session %s with a grant started a child with the display: err = %v; want the authority refusal", holder, err)
		}
	}
	if after, err := e.m.Store.List(true); err != nil || len(after) != len(before) {
		t.Fatalf("refused starts changed sessions: %d before, %d after (%v)", len(before), len(after), err)
	}
	// The grant never passes down: a child of a granted session that asks for
	// nothing keeps the display off.
	for holder := range granted {
		child, err := start(authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: holder, Room: e.room.ID}), false)
		if err != nil {
			t.Fatalf("session %s started a child without the option: %v", holder, err)
		}
		if meta, err := e.m.Store.ReadMeta(child.ID); err != nil || meta.OwnerDisplay {
			t.Fatalf("child of granted session %s inherited the display %v, %v", holder, meta.OwnerDisplay, err)
		}
	}
}
