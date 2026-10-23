package roomsvc

import (
	"dyna-agent/internal/goals"

	"context"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestBulkCloseKeepsOrderAndIndependentPRAndRoomGuards(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("alpha"), e.room("beta")
	author := e.start(alpha, "author", nil)
	free := e.start(alpha, "free", nil)
	if _, err := e.svc.goalService().Store.Finish(alpha.ID, free.Goal, "owner", "Fixture completed", false); err != nil {
		t.Fatal(err)
	}
	other := e.start(beta, "other", nil)
	key := "github.com/owner/repo#17"
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
		key: {Key: key, RoomID: alpha.ID, Author: author.ID, State: "open", Watching: true, Number: 17, URL: "https://github.com/owner/repo/pull/17"},
	}})
	got, err := e.svc.BulkSessionAction(e.ctx, alpha.ID, BulkSessionOptions{Action: "close", Sessions: []string{author.ID, free.ID, other.ID, "missing"}})
	if err != nil || got.Succeeded != 1 || got.Failed != 3 || len(got.Results) != 4 {
		t.Fatalf("batch = %+v, %v", got, err)
	}
	if !strings.HasPrefix(got.Results[0].Error, "responsibilities:") || !got.Results[1].OK || got.Results[1].Member == nil || !got.Results[1].Member.Closed || got.Results[2].OK || got.Results[3].OK {
		t.Fatalf("ordered independent results = %+v", got.Results)
	}
	for _, id := range []string{author.ID, other.ID} {
		info, _ := e.mgr.Store.Info(id)
		if !info.Closed.IsZero() {
			t.Fatalf("guarded session %s was closed", id)
		}
	}
	e.doneGoal(author)
	forced, err := e.svc.BulkSessionAction(e.ctx, alpha.ID, BulkSessionOptions{Action: "close", Sessions: []string{author.ID}, Force: true})
	if err != nil || forced.Succeeded != 1 {
		t.Fatalf("explicit force = %+v, %v", forced, err)
	}
}

func TestBulkValidatesSharedOptionsAndCanonicalDuplicatesBeforeWrites(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	a := e.start(rec, "a", nil)
	for _, opts := range []BulkSessionOptions{
		{Action: "close"},
		{Action: "unknown", Sessions: []string{a.ID}},
		{Action: "transfer", Sessions: []string{a.ID}},
		{Action: "adopt", Sessions: []string{a.ID}},
		{Action: "close", Sessions: []string{a.ID, a.Address()}},
		{Action: "close", Sessions: make([]string, MaxBulkSessions+1)},
	} {
		if _, err := e.svc.BulkSessionAction(e.ctx, rec.ID, opts); err == nil {
			t.Errorf("accepted invalid request %+v", opts)
		}
		info, _ := e.mgr.Store.Info(a.ID)
		if !info.Closed.IsZero() || info.Transfer != nil {
			t.Fatalf("invalid request changed session: %+v", info.Meta)
		}
	}
}

func TestBulkPromoteUsesExistingSemantics(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	parent := e.start(rec, "lead", nil)
	a := e.start(rec, "a", &parent)
	b := e.start(rec, "b", &parent)
	grandchild := e.start(rec, "grandchild", &a)
	got, err := e.svc.BulkSessionAction(e.ctx, rec.ID, BulkSessionOptions{Action: "promote", Sessions: []string{a.ID, b.ID}})
	if err != nil || got.Succeeded != 2 {
		t.Fatalf("promote = %+v, %v", got, err)
	}
	info, _ := e.mgr.Store.Info(grandchild.ID)
	if info.ParentSession != a.ID || info.Depth != 2 {
		t.Fatalf("descendant hierarchy lost: %+v", info.Meta)
	}
}

// "Move under…" in the member panel: each selected session moves under one
// parent, through the same Adopt the CLI uses. A refused move fails alone.
func TestBulkAdoptMovesEachUnderOneParent(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	a := e.start(rec, "a", nil)
	b := e.start(rec, "b", nil)
	got, err := e.svc.BulkSessionAction(e.ctx, rec.ID, BulkSessionOptions{Action: "adopt", Parent: lead.Address(), Sessions: []string{a.ID, b.ID, lead.ID}})
	if err != nil || got.Succeeded != 2 || got.Failed != 1 || !strings.Contains(got.Results[2].Error, "cycle") {
		t.Fatalf("adopt = %+v, %v", got, err)
	}
	for _, s := range []subagent.Info{a, b} {
		info, _ := e.mgr.Store.Info(s.ID)
		if info.ParentSession != lead.ID || info.Depth != 2 {
			t.Fatalf("%s = %+v", s.Name, info.Meta)
		}
	}
	if got.Results[0].Member == nil || got.Results[0].Member.Parent != lead.ID {
		t.Fatalf("member = %+v", got.Results[0].Member)
	}
}

func TestBulkCloseHandsSurvivingChildrenUpWithoutLosingHistory(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &lead)
	leaf := e.start(rec, "leaf", &child)
	e.doneGoal(lead)
	e.doneGoal(child)
	got, err := e.svc.BulkSessionAction(e.ctx, rec.ID, BulkSessionOptions{Action: "close", Sessions: []string{lead.ID, child.ID}, Force: true})
	if err != nil || got.Succeeded != 2 {
		t.Fatalf("close hierarchy = %+v, %v", got, err)
	}
	info, err := e.mgr.Store.Info(leaf.ID)
	if err != nil || !info.Closed.IsZero() || info.ParentSession != "" || info.Depth != 1 {
		t.Fatalf("surviving child = %+v, %v", info.Meta, err)
	}
	if history, err := e.svc.TranscriptTurn(leaf.ID, 1); err != nil || len(history.Turns) != 1 {
		t.Fatalf("surviving history = %+v, %v", history, err)
	}
}

func TestBulkTransferIsDurableAndRetriesOnlyFailedAttempts(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	a, b := e.start(rec, "a", nil), e.start(rec, "b", nil)
	e.mgr.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
	opts := BulkSessionOptions{Action: "transfer", Sessions: []string{a.ID, b.ID}, Profile: "mock"}
	first, err := e.svc.BulkSessionAction(e.ctx, rec.ID, opts)
	if err != nil || first.Succeeded != 2 {
		t.Fatalf("transfer = %+v, %v", first, err)
	}
	for _, result := range first.Results {
		if result.Transfer == nil || result.Transfer.Phase != "waiting" || result.Transfer.Deadline.Sub(result.Transfer.Requested) != 30*time.Second {
			t.Fatalf("durable transfer = %+v", result)
		}
	}
	if _, err := e.mgr.CancelTransfer(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.BulkSessionAction(e.ctx, rec.ID, opts)
	if err != nil || second.Succeeded != 2 || second.Results[0].Transfer.ID == first.Results[0].Transfer.ID || second.Results[1].Transfer.ID != first.Results[1].Transfer.ID {
		t.Fatalf("retry changed a live attempt or did not retry cancellation: %+v, %v", second, err)
	}
}

func TestBulkStopAndCancelledBatchDoNotAffectUnselectedSessions(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	idle, active := e.start(rec, "idle", nil), e.start(rec, "active", nil)
	post, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{active.Address()}, Text: "SLEEP:60000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Wake(e.ctx, active.ID, subagent.WakeOptions{MessageID: post.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.BulkSessionAction(e.ctx, rec.ID, BulkSessionOptions{Action: "stop", Sessions: []string{idle.ID, active.ID}})
	if err != nil || got.Succeeded != 1 || got.Results[0].OK || !got.Results[1].OK {
		t.Fatalf("stop = %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	got, err = e.svc.BulkSessionAction(ctx, rec.ID, BulkSessionOptions{Action: "close", Sessions: []string{idle.ID, active.ID}})
	if err != nil || got.Succeeded != 0 || got.Failed != 2 {
		t.Fatalf("cancelled close = %+v, %v", got, err)
	}
	for _, id := range []string{idle.ID, active.ID} {
		info, _ := e.mgr.Store.Info(id)
		if !info.Closed.IsZero() {
			t.Fatal("cancelled batch closed a session")
		}
	}
}

func TestBulkActionsKeepConsolidationReservations(t *testing.T) {
	for _, action := range []string{"stop", "close", "promote", "transfer"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("alpha")
			// The reservation guards read stored hierarchy and status only, so the
			// reserved source needs no harness turn.
			seed := func(name string, parent *subagent.Info, status string) subagent.Info {
				meta := subagent.Meta{ID: subagent.NewID(), Name: name, RoomID: rec.ID, Room: rec.Name,
					Profile: "mock", Status: status, Dir: t.TempDir(), Created: time.Now(), Depth: 1}
				if parent != nil {
					meta.ParentSession, meta.Depth = parent.ID, parent.Depth+1
				}
				recordtest.Write(t, e.mgr.Store.Root, meta.ID, meta)
				return subagent.Info{Meta: meta, Status: meta.Status}
			}
			lead := seed("lead", nil, subagent.StatusIdle)
			target := seed("target", &lead, subagent.StatusIdle)
			// A stop refuses a session that is not running before it reads the
			// reservation, so the reserved source needs a running row to reach
			// the same guard as the other actions.
			sourceStatus := subagent.StatusIdle
			if action == "stop" {
				sourceStatus = subagent.StatusRunning
			}
			source := seed("source", &lead, sourceStatus)
			// The free session is created exactly once: Start refuses a name that
			// a seeded row already holds in the same room.
			var free subagent.Info
			if action == "stop" {
				// A child delegates from its parent's goal.
				if _, err := e.svc.goalService().Store.Add(rec.ID, "owner", goals.Input{Text: "Work", Holders: []string{lead.ID}}); err != nil {
					t.Fatal(err)
				}
				// The stopped session's first turn is the long one, so the case
				// needs no completed setup turn and no wake before the batch.
				e.as(&lead)
				info, _, err := e.mgr.Start(context.Background(), subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Name: "free", Dir: t.TempDir(), Task: "SLEEP:60000"})
				e.as(nil)
				if err != nil {
					t.Fatalf("Start free: %v", err)
				}
				free = info
			} else {
				free = seed("free", &lead, subagent.StatusIdle)
			}
			e.mgr.ConsolidationLaunch = func(*subagent.Manager, string, string) error { return nil }
			e.mgr.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
			operation, err := e.mgr.Consolidate(context.Background(), subagent.ConsolidationOptions{Sessions: []string{target.ID, source.ID}, Target: target.ID})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = e.mgr.CancelConsolidation(context.Background(), rec.ID, operation.ID)
				if action == "stop" {
					_, _ = e.mgr.Stop(e.ctx, free.ID)
				}
			}()
			before, err := e.mgr.Store.ReadMeta(source.ID)
			if err != nil || before.Transfer == nil {
				t.Fatalf("missing source reservation: %+v, %v", before, err)
			}
			got, err := e.svc.BulkSessionAction(e.ctx, rec.ID, BulkSessionOptions{Action: action, Sessions: []string{source.ID, free.ID}, Profile: "mock", Force: true})
			if err != nil || got.Succeeded != 1 || got.Failed != 1 || got.Results[0].OK || !got.Results[1].OK {
				t.Fatalf("reserved and free batch = %+v, %v", got, err)
			}
			if !strings.Contains(got.Results[0].Error, "consolidation") {
				t.Fatalf("reserved source was refused for another reason: %q", got.Results[0].Error)
			}
			meta, err := e.mgr.Store.ReadMeta(source.ID)
			if err != nil || meta.Consolidating != operation.ID || !meta.Closed.IsZero() || meta.ParentSession != lead.ID || meta.Transfer == nil || meta.Transfer.ID != before.Transfer.ID {
				t.Fatalf("bulk action changed reserved source: %+v, %v", meta, err)
			}
		})
	}
}
