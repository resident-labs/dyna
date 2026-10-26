package subagent

import (
	"context"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/questions"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
)

func consolidationEnv(t *testing.T) (*testEnv, *time.Time) {
	e, now := transferEnv(t)
	e.m.ConsolidationLaunch = func(*Manager, string, string) error { return nil }
	return e, now
}
func consolidatePair(t *testing.T, e *testEnv, a, b Info) ConsolidationState {
	t.Helper()
	c, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID, Profile: "next"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func consolidationStep(t *testing.T, e *testEnv, c ConsolidationState) ConsolidationState {
	t.Helper()
	fresh, err := e.m.consolidation(c.RoomID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.advanceConsolidation(e.ctx, fresh); err != nil {
		t.Fatal(err)
	}
	fresh, err = e.m.consolidation(c.RoomID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}
func finishConsolidation(t *testing.T, e *testEnv, c ConsolidationState) ConsolidationState {
	t.Helper()
	for i := 0; i < 10; i++ {
		c = consolidationStep(t, e, c)
		if c.Phase == "starting" {
			meta, _ := e.m.Store.ReadMeta(c.Target)
			if meta.Transfer != nil && meta.Turns >= meta.Transfer.Turn {
				e.wait(c.Target, meta.Transfer.Turn)
			}
		}
		if c.Phase == "completed" {
			return c
		}
	}
	t.Fatalf("did not finish: %+v", c)
	return c
}
func submitAll(t *testing.T, e *testEnv, c ConsolidationState) {
	t.Helper()
	for _, source := range c.Sources {
		if err := e.m.SubmitHandoff(context.Background(), source.Session, consolidationToken(c.ID, source.Session), "Written task handoff for "+source.Address+". Preserve parser work.", "", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConsolidationFailedSourcesImmediateAndRetryRestoresNativeConversation(t *testing.T) {
	e, now := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: target")
	a, old := e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: source")
	b, _ = e.wait(b.ID, 1)
	for _, id := range []string{a.ID, b.ID} {
		_, err := e.m.Store.UpdateTurn(id, 1, func(turn *Turn) { turn.Status = TurnError; turn.Error = "provider quota exceeded" })
		if err != nil {
			t.Fatal(err)
		}
	}
	c := consolidatePair(t, e, a, b)
	c = consolidationStep(t, e, c)
	if c.Phase != "stopping" || !e.m.transferNow().Equal(*now) {
		t.Fatalf("failed agent waited: %+v", c)
	}
	for _, id := range c.Sessions {
		meta, _ := e.m.Store.ReadMeta(id)
		if meta.Transfer.Posted {
			t.Fatal("failed source received handoff request")
		}
	}
	c = consolidationStep(t, e, c)
	// Keep retirement tracking when the temporary spawner is removed.
	runner := e.m.Spawner
	e.m.Spawner = failedTransferSpawner{}
	c = consolidationStep(t, e, c)
	if c.Phase != "failed" {
		t.Fatalf("startup did not fail: %+v", c)
	}
	posts, err := e.m.rooms().Messages(c.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	alerted := false
	for _, post := range posts {
		if alert, ok := post.Alert(); ok && alert.Source == room.AlertConsolidation && strings.Contains(alert.Detail, "phase starting") {
			alerted = true
		}
	}
	if !alerted {
		t.Fatal("startup failure did not alert root")
	}
	meta, _ := e.m.Store.ReadMeta(a.ID)
	source, _ := e.m.Store.ReadMeta(b.ID)
	if meta.HarnessSession != old.HarnessSession || meta.Profile != "mock" || !source.Closed.IsZero() || source.ConsumedBy != "" || source.Consolidating != c.ID {
		t.Fatal("startup failure consumed source or lost old native context")
	}
	savedContext := c.Sources[0].Context
	e.m.Spawner = runner
	if _, err := e.m.ResumeConsolidation(context.Background(), c.RoomID, c.ID); err != nil {
		t.Fatal(err)
	}
	c = finishConsolidation(t, e, c)
	if c.Sources[0].Context != savedContext {
		t.Fatal("retry replaced the durable source context")
	}
	meta, _ = e.m.Store.ReadMeta(a.ID)
	if meta.Turns != 3 {
		t.Fatalf("replacement retry count: %d", meta.Turns)
	}
}

func TestConsolidationPartialReservationCancelAndCoordinatorRetries(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	c := consolidatePair(t, e, a, b)
	// Simulate death after durable plan but before this reservation write.
	_, _ = e.m.Store.UpdateMeta(b.ID, func(meta *Meta) { meta.Consolidating = ""; meta.Transfer = nil })
	_, _ = e.m.Store.UpdateTurn(b.ID, 1, func(turn *Turn) { turn.Profile = ""; turn.Model = "" })
	if err := e.m.reserveConsolidation(c); err != nil {
		t.Fatal(err)
	}
	oldTurn, _ := e.m.Store.ReadTurn(b.ID, 1)
	if oldTurn.Profile != "mock" || oldTurn.Model != "old-model" {
		t.Fatal("reservation recovery lost old turn profile attribution")
	}
	meta, _ := e.m.Store.ReadMeta(b.ID)
	if meta.Consolidating != c.ID || meta.Transfer == nil {
		t.Fatal("partial reservation not recovered")
	}
	if _, err := e.send(b.ID, "SLEEP:1 pending input", WakeOptions{}); err == nil {
		t.Fatal("reserved source started a new turn")
	}
	if _, err := e.m.TransferContext(context.Background(), b.ID, TransferOptions{Profile: "next"}); err == nil {
		t.Fatal("competing transfer accepted")
	}
	if _, err := e.m.Promote(context.Background(), b.ID); err == nil {
		t.Fatal("reserved promotion accepted")
	}
	if _, err := e.m.CloseWithOptions(e.ctx, b.ID, CloseOptions{Force: true}); err == nil {
		t.Fatal("force close bypassed reservation")
	}
	// Simulate cancellation durable record before one reservation was cleared.
	if err := e.m.updateConsolidation(a.ID, c.ID, func(s *ConsolidationState) error { s.Phase = "cancelled"; s.CancelPending = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.ResumeConsolidation(context.Background(), c.RoomID, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range c.Sessions {
		meta, _ := e.m.Store.ReadMeta(id)
		if meta.Consolidating != "" || !meta.Closed.IsZero() {
			t.Fatal("cancel stranded or consumed source")
		}
	}
	status, _ := e.m.consolidation(c.RoomID, c.ID)
	if status.CancelPending || status.Phase != "cancelled" {
		t.Fatal("cleanup not complete")
	}
}

func TestConsolidationHierarchySelectedAncestorsAndCycleGuard(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: external")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: ancestor")
	b, _ = e.wait(b.ID, 1)
	middle, _ := e.start("SLEEP:1 RESPOND: middle")
	middle, _ = e.wait(middle.ID, 1)
	target, _ := e.start("SLEEP:1 RESPOND: target")
	target, _ = e.wait(target.ID, 1)
	leaf, _ := e.start("SLEEP:1 RESPOND: leaf")
	leaf, _ = e.wait(leaf.ID, 1)
	for _, pair := range [][2]string{{b.ID, a.ID}, {middle.ID, b.ID}, {target.ID, middle.ID}, {leaf.ID, target.ID}} {
		_, _ = e.m.Store.UpdateMeta(pair[0], func(meta *Meta) { meta.ParentSession = pair[1] })
	}
	c := consolidatePair(t, e, target, b)
	submitAll(t, e, c)
	finishConsolidation(t, e, c)
	for id, want := range map[string]struct {
		parent string
		depth  int
	}{target.ID: {a.ID, 2}, middle.ID: {target.ID, 3}, leaf.ID: {target.ID, 3}} {
		meta, _ := e.m.Store.ReadMeta(id)
		if meta.ParentSession != want.parent || meta.Depth != want.depth {
			t.Fatalf("hierarchy: %s %+v", id, meta)
		}
	}
	// Bad old metadata must be rejected before any hierarchy plan is written.
	_, _ = e.m.Store.UpdateMeta(a.ID, func(meta *Meta) { meta.ParentSession = middle.ID })
	c = ConsolidationState{RoomID: a.RoomID, Target: target.ID, Sessions: []string{target.ID, leaf.ID}}
	if err := e.m.consolidationHierarchy(c); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestConsolidationConcurrentRequestsHaveOneDurableOperation(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID, Profile: "next"})
			ids <- c.ID
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	one := ""
	for id := range ids {
		if one != "" && one != id {
			t.Fatal("duplicate operation")
		}
		one = id
	}
}

func TestConsolidationRejectsInvalidSelectionAndLaunchFailureIsRecoverable(t *testing.T) {
	e, _ := consolidationEnv(t)
	a := completedFixtureSession(t, e, "SLEEP:1 RESPOND: a")
	b := completedFixtureSession(t, e, "SLEEP:1 RESPOND: b")
	for _, opts := range []ConsolidationOptions{{Sessions: []string{a.ID}, Target: a.ID}, {Sessions: []string{a.ID, a.ID}, Target: a.ID}, {Sessions: []string{a.ID, b.ID}, Target: "ses_missing"}} {
		if _, err := e.m.Consolidate(context.Background(), opts); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	e.m.ConsolidationLaunch = func(*Manager, string, string) error { return errors.New("fake process failure") }
	c, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID})
	if err == nil || c.ID == "" {
		t.Fatal("launch failure lost durable plan")
	}
	e.m.ConsolidationLaunch = func(*Manager, string, string) error { return nil }
	if _, err = e.m.ResumeConsolidation(context.Background(), c.RoomID, c.ID); err != nil {
		t.Fatal(err)
	}
	submitAll(t, e, c)
	finishConsolidation(t, e, c)
}

func TestConsolidationInheritedInboxClaimsAndCancellation(t *testing.T) {
	e, now := consolidationEnv(t)
	e.m.Store.Now = func() time.Time { return *now }
	a, _ := e.start("SLEEP:1 RESPOND: target")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: source b")
	b, _ = e.wait(b.ID, 1)
	other, _ := e.start("SLEEP:1 RESPOND: source c")
	other, _ = e.wait(other.ID, 1)
	c, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID, other.ID}, Target: a.ID, Profile: "next"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := e.m.rooms().Post(a.RoomID, room.PostInput{From: a.Room, To: []string{b.Address(), other.Address()}, Text: "shared deferred source request"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := e.m.rooms().Post(a.RoomID, room.PostInput{From: a.Room, To: []string{b.Address(), other.Address()}, Text: "cancel one origin only"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.m.Store.DeletePendingSteer(e.m.rooms(), b.ID, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	// A dispatched but unacknowledged source input remains pending.
	pending, err := e.m.rooms().Post(a.RoomID, room.PostInput{From: a.Room, To: []string{b.Address()}, Text: "claimed without acknowledgement"})
	if err != nil {
		t.Fatal(err)
	}
	claimPostsFor(t, e, b.ID, 1, pending.ID)
	// A stopping runner may have advanced its cursor without acknowledgement.
	_, at, _ := e.m.rooms().ReadFrom(a.RoomID, 0)
	_, _ = e.m.Store.UpdateMeta(b.ID, func(meta *Meta) { meta.RoomCursor = at })
	*now = c.Deadline
	c = finishConsolidation(t, e, c)
	meta, _ := e.m.Store.ReadMeta(a.ID)
	msgs, err := e.m.Store.inboxMessages(e.m.rooms(), meta, nil)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("pending claims: %+v %v", msgs, err)
	}
	// Persist receipt once in target, preserving original source evidence.
	_, _ = e.m.Store.UpdateTurn(a.ID, meta.Turns, func(turn *Turn) {
		turn.Steering = append(turn.Steering, Steer{ID: shared.ID, Delivered: true}, Steer{ID: cancelled.ID, Delivered: true})
	})
	*now = now.Add(2 * time.Minute)
	meta, _ = e.m.Store.ReadMeta(a.ID)
	msgs, err = e.m.Store.inboxMessages(e.m.rooms(), meta, nil)
	if err != nil || len(msgs) != 1 || msgs[0].ID != pending.ID {
		t.Fatalf("origin replayed shared post or lost unacknowledged claim: %+v %v", msgs, err)
	}
	originalQueue, err := e.m.Store.SessionQueue(e.m.rooms(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range originalQueue {
		if item.ID == cancelled.ID && item.State != "cancelled" {
			t.Fatal("per-origin cancellation lost")
		}
	}
	// A post appended to an old address after the stop snapshot remains readable.
	late, err := e.m.rooms().Post(a.RoomID, room.PostInput{From: a.Room, To: []string{b.Address()}, Text: "late source post"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err = e.m.Store.inboxMessages(e.m.rooms(), meta, nil)
	found := false
	for _, msg := range msgs {
		if msg.ID == late.ID {
			found = true
		}
	}
	if err != nil || !found {
		t.Fatal("late source-address post stranded")
	}
}

func TestConsolidationMigratesLivePRWithoutForceClose(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: kept task")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: author task")
	b, _ = e.wait(b.ID, 1)
	writePRLink(t, b, "author", "open", true)
	if _, err := e.m.CloseWithOptions(e.ctx, b.ID, CloseOptions{}); err == nil {
		t.Fatal("fixture missing close guard")
	}
	c := consolidatePair(t, e, a, b)
	submitAll(t, e, c)
	finishConsolidation(t, e, c)
	links, err := e.m.TrackedPRs(a.ID)
	if err != nil || len(links) != 1 || !links[0].Active() {
		t.Fatalf("live PR continuity: %+v %v", links, err)
	}
	if _, err = e.m.CloseWithOptions(e.ctx, a.ID, CloseOptions{}); err == nil {
		t.Fatal("kept author lost close guard")
	}
}

// The kept session continues absorbed work, so it inherits the absorbed
// session's goals and open questions through the same responsibility list.
func TestConsolidationReturnsGoalsAndMovesQuestions(t *testing.T) {
	e, _ := consolidationEnv(t)
	a := completedFixtureSession(t, e, "SLEEP:0 RESPOND: kept task")
	b := completedFixtureSession(t, e, "SLEEP:0 RESPOND: absorbed task")
	delegated, err := e.m.goalStore().Add(b.RoomID, "owner", goals.Input{Text: "Delegated absorbed work", Holders: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	own, err := e.m.goalStore().Add(b.RoomID, b.ID, goals.Input{Text: "Absorbed session's own work", Holders: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.m.questionStore().Ask(questions.Question{Room: b.RoomID, Asker: b.ID, To: "owner", Text: "Which option?", Meanwhile: "continue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := consolidatePair(t, e, a, b)
	submitAll(t, e, c)
	finishConsolidation(t, e, c)
	kept, _ := e.m.Store.Info(a.ID)
	items, err := e.m.Responsibilities(e.ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range items {
		got[string(r.Kind)+" "+r.ID] = true
	}
	// Goals do not transfer as a rule. The delegated goal has no live setter,
	// so the kept session, the close heir, holds it. b's own goal drops with
	// it, and only the question moves to the kept session.
	if got["goal "+own.ID] || !got["goal "+delegated.ID] || !got["question "+q.ID] {
		t.Fatalf("kept session responsibilities = %+v, want the delegated goal and question %s", items, q.ID)
	}
	if g, err := e.m.goalStore().Get(b.RoomID, delegated.ID); err != nil || !g.Open() || !g.HeldBy(a.ID) {
		t.Fatalf("delegated goal = %+v err=%v", g, err)
	}
	if g, err := e.m.goalStore().Get(b.RoomID, own.ID); err != nil || g.State != "dropped" {
		t.Fatalf("own goal = %+v err=%v", g, err)
	}
}

func TestConsolidationCommitRecoveryDoesNotStartAnotherTarget(t *testing.T) {
	e, now := consolidationEnv(t)
	e.m.Store.Now = func() time.Time { return *now }
	a, _ := e.start("SLEEP:1 RESPOND: kept")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: source")
	b, _ = e.wait(b.ID, 1)
	c := consolidatePair(t, e, a, b)
	if err := e.m.SubmitHandoff(context.Background(), a.ID, consolidationToken(c.ID, a.ID), "Continue kept task.", "", ""); err != nil {
		t.Fatal(err)
	}
	*now = c.Deadline
	for i := 0; i < 8 && c.Phase != "completed"; i++ {
		c = consolidationStep(t, e, c)
		if c.Phase == "starting" {
			meta, _ := e.m.Store.ReadMeta(a.ID)
			if meta.Turns >= meta.Transfer.Turn {
				e.wait(a.ID, meta.Transfer.Turn)
			}
		}
	}
	if c.Phase != "completed" {
		t.Fatal(c.Phase)
	}
	// Model a restart after commit writes, before the completed phase persisted.
	if err := e.m.updateConsolidation(a.ID, c.ID, func(state *ConsolidationState) error {
		state.Phase, state.Ended = "committing", time.Time{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(a.ID)
	turnCount := meta.Turns
	if _, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: b.ID}); err == nil {
		t.Fatal("competing operation used consumed source")
	}
	if err := e.m.runConsolidation(e.ctx, a.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	meta, _ = e.m.Store.ReadMeta(a.ID)
	if meta.Turns != turnCount || meta.Consolidation.Phase != "completed" || len(meta.InheritedInboxes) != 1 {
		t.Fatalf("commit replay duplicated target/inbox: %+v", meta)
	}
}

func TestConsolidationRejectsClosedCrossRoomAndCyclesBeforeConsumption(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	_, _ = e.m.Store.UpdateMeta(b.ID, func(meta *Meta) { meta.RoomID = "room_other" })
	if _, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID}); err == nil {
		t.Fatal("cross room accepted")
	}
	_, _ = e.m.Store.UpdateMeta(b.ID, func(meta *Meta) { meta.RoomID = a.RoomID; meta.Closed = time.Now() })
	if _, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed selection: %v", err)
	}
}

func TestConsolidationRejectsCyclicSelectionBeforeReservation(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	_, _ = e.m.Store.UpdateMeta(a.ID, func(meta *Meta) { meta.ParentSession = b.ID })
	_, _ = e.m.Store.UpdateMeta(b.ID, func(meta *Meta) { meta.ParentSession = a.ID })
	if _, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{a.ID, b.ID}, Target: a.ID}); err == nil {
		t.Fatal("cyclic selection started a handoff")
	}
	for _, id := range []string{a.ID, b.ID} {
		meta, _ := e.m.Store.ReadMeta(id)
		if meta.Consolidating != "" || meta.Transfer != nil {
			t.Fatal("invalid hierarchy left a reservation")
		}
	}
}

func TestConsolidationDoesNotReplayUnacknowledgedHandoffRequest(t *testing.T) {
	e, now := consolidationEnv(t)
	e.m.Store.Now = func() time.Time { return *now }
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	c := consolidatePair(t, e, a, b)
	meta, _ := e.m.Store.ReadMeta(b.ID)
	request, err := e.m.rooms().Post(b.RoomID, room.PostInput{ID: meta.Transfer.PostID, From: b.Room, To: []string{b.Address()}, Text: "obsolete model handoff request"})
	if err != nil {
		t.Fatal(err)
	}
	claimPostsFor(t, e, b.ID, 1, request.ID)
	*now = c.Deadline
	finishConsolidation(t, e, c)
	*now = now.Add(time.Minute)
	meta, _ = e.m.Store.ReadMeta(a.ID)
	msgs, err := e.m.Store.inboxMessages(e.m.rooms(), meta, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range msgs {
		if msg.ID == request.ID {
			t.Fatal("obsolete handoff instruction replayed")
		}
	}
}

func TestConsolidationCancellationDoesNotTouchReleasedSourceLaterTransfer(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed-no-op", true: "partial-cleanup-retry"}[partial], func(t *testing.T) {
			e, _ := consolidationEnv(t)
			a, _ := e.start("SLEEP:1 RESPOND: kept")
			a, _ = e.wait(a.ID, 1)
			b, _ := e.start("SLEEP:1 RESPOND: source")
			b, _ = e.wait(b.ID, 1)
			c := consolidatePair(t, e, a, b)
			ended, err := e.m.CancelConsolidation(context.Background(), c.RoomID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			next, err := e.m.TransferContext(context.Background(), b.ID, TransferOptions{Profile: "next", Retry: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.m.rooms().Post(b.RoomID, room.PostInput{ID: next.PostID, From: b.Room, To: []string{b.Address()}, Text: "new unrelated transfer handoff request"})
			if err != nil {
				t.Fatal(err)
			}
			if partial {
				_, _ = e.m.Store.UpdateMeta(a.ID, func(meta *Meta) {
					meta.Consolidating = c.ID
					meta.Consolidation.CancelPending = true
					meta.Consolidation.Ended = time.Time{}
				})
			}
			result, err := e.m.CancelConsolidation(context.Background(), c.RoomID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if result.CancelPending {
				t.Fatal("partial cleanup did not finish")
			}
			if !partial && !result.Ended.Equal(ended.Ended) {
				t.Fatal("completed cancellation changed its result")
			}
			current, err := e.m.TransferStatus(b.ID)
			if err != nil || current.ID != next.ID || current.Phase != "waiting" {
				t.Fatalf("later transfer changed: %+v %v", current, err)
			}
			queue, err := e.m.Store.SessionQueue(e.m.rooms(), b.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range queue {
				if item.ID == next.PostID {
					found = true
					if item.State != "waiting" {
						t.Fatalf("old cancellation changed later request: %+v", item)
					}
				}
			}
			if !found {
				t.Fatal("later request disappeared")
			}
		})
	}
}

func TestConsolidationSkipsUnassignedLegacyHistory(t *testing.T) {
	e, _ := consolidationEnv(t)
	paths := map[string][]byte{}
	for _, closed := range []bool{false, true} {
		id := NewID()
		raw := []byte(`{"id":"` + id + `","historyOnly":true,"name":"old"}`)
		if closed {
			raw = []byte(`{"id":"` + id + `","historyOnly":true,"name":"old","closed":"2026-10-05T00:00:00Z"}`)
		}
		paths[writeRoomScanFixture(t, e, id, raw)] = raw
	}
	a, _ := e.start("SLEEP:1 RESPOND: target")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: source")
	b, _ = e.wait(b.ID, 1)
	c := consolidatePair(t, e, a, b)
	submitAll(t, e, c)
	c = finishConsolidation(t, e, c)
	if c.Phase != "completed" {
		t.Fatalf("consolidation: %+v", c)
	}
	for path, raw := range paths {
		got, err := storedRawForTest(t, e.m.Store.Root, filepath.Base(filepath.Dir(path)))
		if err != nil || string(got) != string(raw) {
			t.Fatalf("legacy record changed: %s, %v", got, err)
		}
	}
}

func TestConsolidationRejectsDamagedRoomEvidence(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `{}`, `{"room":"global"}`, `{"roomId":"global"}`, `{"room":null,"roomId":null}`, `{"room":"","roomId":""}`} {
		t.Run(raw, func(t *testing.T) {
			s := &Store{Root: t.TempDir()}
			id := NewID()
			if strings.HasPrefix(raw, `{"room`) {
				raw = `{"id":"` + id + `",` + raw[1:]
			}
			recordtest.WriteRaw(t, s.Root, id, []byte(raw))
			m := &Manager{Store: s}
			err := m.consolidationHierarchyPlan(ConsolidationState{RoomID: "global"}, false)
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("damaged record ignored: %v", err)
			}
			after, _ := storedRawForTest(t, s.Root, id)
			if string(after) != raw {
				t.Fatal("damaged record rewritten")
			}
		})
	}
}

func TestConsolidationRefusesInternalRoot(t *testing.T) {
	e, _ := consolidationEnv(t)
	root, _ := e.start("SLEEP:1 RESPOND: root", func(o *StartOptions) { o.Role, o.Name = RoleRoot, RoleRoot })
	root, _ = e.wait(root.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	for _, target := range []string{root.ID, b.ID} {
		_, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: []string{root.ID, b.ID}, Target: target})
		if err == nil || !strings.Contains(err.Error(), "internal root") {
			t.Fatalf("consolidating the internal root: got %v, want a refusal that names the internal root", err)
		}
	}
}
