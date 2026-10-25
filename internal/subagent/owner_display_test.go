package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
)

// TestOwnerDisplayGrantNeedsRootOrOwner proves the owner's display is never an
// agent's to take: the owner and root may grant it to a new session, it is off
// by default, and an agent session cannot grant it, not even a session that
// already holds a grant.
//
// A holder of a grant stays in its first turn and owns an open goal, so it is
// never dormant and no dormancy check can be checking it. Such a check refuses
// new work with "session is being checked for dormant close", and whether it had
// begun depended on how busy the machine was, so the test must not race it. The
// hold ends when the test's context ends.
const holderTask = "SLEEP:600000\nRESPOND: ready"

func TestOwnerDisplayGrantNeedsRootOrOwner(t *testing.T) {
	e := newTestEnv(t)
	start := func(ctx context.Context, task string, grant bool) (Info, error) {
		info, _, err := e.m.Start(ctx, StartOptions{
			Goal: "Finish the test task", Profile: "mock", Dir: t.TempDir(),
			Task: task, OwnerDisplay: grant,
		})
		return info, err
	}
	granted := map[string]bool{}
	for _, caller := range []authority.Actor{{Role: authority.Owner}, {Role: authority.Root}} {
		info, err := start(authority.WithActor(context.Background(), caller), holderTask, true)
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
		if _, err := e.m.goalStore().Add(info.RoomID, info.ID, goals.Input{Text: "Open work", Holders: []string{info.ID}}); err != nil {
			t.Fatal(err)
		}
		granted[info.ID] = true
	}
	plain, err := start(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}), "SLEEP:0\nRESPOND: ready", false)
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
		_, err := start(authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: holder, Room: e.room.ID}), "SLEEP:0\nRESPOND: ready", true)
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
		// A session with open work is never dormant, which is the state this test
		// needs: nothing is checking the holder here.
		info, err := e.m.Store.Info(holder)
		if err != nil {
			t.Fatal(err)
		}
		if items, err := e.m.Responsibilities(context.Background(), info); err != nil || len(items) == 0 {
			t.Fatalf("granted session %s owns no work before the child start: %v, %v", holder, len(items), err)
		}
		child, err := start(authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: holder, Room: e.room.ID}), "SLEEP:0\nRESPOND: ready", false)
		if err != nil {
			t.Fatalf("session %s started a child without the option: %v", holder, err)
		}
		if meta, err := e.m.Store.ReadMeta(child.ID); err != nil || meta.OwnerDisplay {
			t.Fatalf("child of granted session %s inherited the display %v, %v", holder, meta.OwnerDisplay, err)
		}
	}
}
