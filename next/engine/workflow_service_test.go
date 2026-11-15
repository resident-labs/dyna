package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// serviceCase is a service whose box the test plays: it asks for events and acknowledges them as a box would.
type serviceCase struct {
	workflowCase
	sub   contract.SubscriptionID
	clock *clock
	held  map[int64]bool // the rows the box holds, which it reports with each request
}

func newService(t *testing.T, latest bool) serviceCase {
	t.Helper()
	w := setupWorkflow(t)
	_, clock := withRuns(t, w.Service)
	sub := addSubscription(t, w.Service, w.ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: w.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: serviceScript}, Latest: latest})
	return serviceCase{w, sub, clock, map[int64]bool{}}
}

func (s serviceCase) fire(key string) {
	s.t.Helper()
	if _, problem := s.SubscriptionFire(s.ctx, s.owner, contract.SubscriptionFireRequest{ID: s.sub, Key: &key}); problem != nil {
		s.t.Fatal(problem)
	}
}

// run starts the service if it has no run, and returns the run and the box's identity.
func (s serviceCase) run() (store.WorkflowRun, contract.Principal) {
	s.t.Helper()
	if err := s.fireDue(s.ctx); err != nil {
		s.t.Fatal(err)
	}
	run, err := s.store.ServiceRun(s.ctx, s.sub)
	if err != nil {
		s.t.Fatal(err)
	}
	return run, sessionCaller(run.Session)
}

func (s serviceCase) poll(box contract.Principal, boot string, limit int) []contract.WorkflowEvent {
	s.t.Helper()
	events := s.ask(box, boot, limit)
	for _, event := range events {
		s.held[event.Row] = true
	}
	return events
}

// ask is a request whose reply the box may never read.
func (s serviceCase) ask(box contract.Principal, boot string, limit int) []contract.WorkflowEvent {
	s.t.Helper()
	var held []int64
	for row := range s.held {
		held = append(held, row)
	}
	got, problem := s.WorkflowEvents(s.ctx, box, contract.WorkflowEventsRequest{Boot: boot, Limit: limit, Held: held})
	if problem != nil {
		s.t.Fatal(problem)
	}
	return got.Events
}

func (s serviceCase) done(box contract.Principal, row int64, failure string) {
	s.t.Helper()
	if _, problem := s.WorkflowEventDone(s.ctx, box, contract.WorkflowEventDoneRequest{Row: row, Error: failure}); problem != nil {
		s.t.Fatal(problem)
	}
	delete(s.held, row)
}

func (s serviceCase) notices(match string) int {
	s.t.Helper()
	var n int
	if err := s.store.Read().QueryRowContext(s.ctx, "SELECT count(*) FROM events WHERE type='notice.posted' AND CAST(body AS TEXT) LIKE ?", "%"+match+"%").Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func keys(events []contract.WorkflowEvent) string {
	var out []string
	for _, event := range events {
		out = append(out, event.Key)
	}
	return strings.Join(out, ",")
}

func TestServiceHasOneRunAndHandsOverSerialPerKey(t *testing.T) {
	s := newService(t, false)
	s.fire("a")
	s.fire("a")
	s.fire("b")
	run, box := s.run()
	again, _ := s.run()
	if again.ID != run.ID || run.Subscription != s.sub || run.Key != "" {
		t.Fatalf("two runs: %+v %+v", run, again)
	}
	first := s.poll(box, "boot1", 10)
	if keys(first) != "a,b" {
		t.Fatalf("one row for each idle key, got %s", keys(first))
	}
	if more := s.poll(box, "boot1", 10); len(more) != 0 {
		t.Fatalf("a key with a row in a handler gets no second: %s", keys(more))
	}
	s.done(box, first[0].Row, "")
	next := s.poll(box, "boot1", 10)
	if keys(next) != "a" || next[0].Row == first[0].Row {
		t.Fatalf("the next row of a after its ack: %s", keys(next))
	}
	s.done(box, first[1].Row, "")
	s.done(box, next[0].Row, "")
	info, problem := s.WorkflowStatus(s.ctx, s.owner, contract.WorkflowStatusRequest{Run: run.ID})
	if problem != nil {
		t.Fatal(problem)
	}
	if svc := info.Run.Service; svc == nil || svc.Handled != 3 || svc.Failed != 0 || svc.State != contract.WorkflowServiceServing {
		t.Fatalf("service %+v", svc)
	}
}

func TestServiceLatestReplacesAWaitingRowWhileOneIsInFlight(t *testing.T) {
	s := newService(t, true)
	s.fire("a")
	_, box := s.run()
	first := s.poll(box, "boot1", 10)
	s.fire("a")
	s.fire("a")
	s.fire("a")
	info, problem := s.WorkflowStatus(s.ctx, s.owner, contract.WorkflowStatusRequest{Session: box.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	if got := info.Run.Service.Keys; len(got) != 1 || got[0].Waiting != 1 || got[0].InFlight != 1 {
		t.Fatalf("keys %+v", got)
	}
	s.done(box, first[0].Row, "")
	if next := s.poll(box, "boot1", 10); len(next) != 1 {
		t.Fatalf("the newest waiting row, once: %s", keys(next))
	}
}

func TestServiceRedeliversAfterABoxRestartAndFailsAfterThreeHandOvers(t *testing.T) {
	s := newService(t, false)
	s.fire("a")
	_, box := s.run()
	var row int64
	for _, boot := range []string{"boot1", "boot2", "boot3"} {
		got := s.poll(box, boot, 10)
		if len(got) != 1 || row != 0 && got[0].Row != row {
			t.Fatalf("%s: %+v", boot, got)
		}
		row = got[0].Row
	}
	if got := s.poll(box, "boot4", 10); len(got) != 0 {
		t.Fatalf("a fourth hand-over: %+v", got)
	}
	if s.notices("took it 3 times") != 1 {
		t.Fatal("the parent was not told of the dropped row")
	}
	var n int
	if err := s.store.Read().QueryRowContext(s.ctx, "SELECT count(*) FROM workflow_queue").Scan(&n); err != nil || n != 0 {
		t.Fatalf("row left in the queue: %d %v", n, err)
	}
	run, _ := s.run()
	if run.Served.Failed != 1 {
		t.Fatalf("served %+v", run.Served)
	}
}

func TestServiceHandlerErrorIsAcknowledgedAndToldOncePerStreak(t *testing.T) {
	s := newService(t, false)
	_, box := s.run()
	failOne := func(key, reason string) {
		s.fire(key)
		got := s.poll(box, "boot1", 10)
		s.done(box, got[0].Row, reason)
	}
	failOne("a", "boom")
	failOne("a", "boom again")
	failOne("b", "other key")
	if s.notices("the handler for key") != 2 {
		t.Fatalf("notices %d, want one for each key's streak", s.notices("the handler for key"))
	}
	s.fire("a")
	got := s.poll(box, "boot1", 10)
	s.done(box, got[0].Row, "")
	failOne("a", "new streak")
	if s.notices("the handler for key") != 3 {
		t.Fatal("a success did not end the streak")
	}
	run, _ := s.run()
	if run.Served.Failed != 4 || run.Served.Handled != 1 {
		t.Fatalf("served %+v", run.Served)
	}
}

func TestServiceStateBelongsToTheRowsKey(t *testing.T) {
	s := newService(t, false)
	s.fire("a")
	s.fire("b")
	_, box := s.run()
	got := s.poll(box, "boot1", 10)
	set := func(row int64, value string) *contract.Error {
		_, problem := s.WorkflowState(s.ctx, box, contract.WorkflowStateRequest{Row: row, Value: json.RawMessage(value)})
		return problem
	}
	if problem := set(got[0].Row, `{"head":"abc"}`); problem != nil {
		t.Fatal(problem)
	}
	read, problem := s.WorkflowState(s.ctx, box, contract.WorkflowStateRequest{Row: got[1].Row})
	if problem != nil || string(read.Value) != "null" {
		t.Fatalf("other key's state %s %v", read.Value, problem)
	}
	read, _ = s.WorkflowState(s.ctx, box, contract.WorkflowStateRequest{Row: got[0].Row})
	if string(read.Value) != `{"head":"abc"}` {
		t.Fatalf("state %s", read.Value)
	}
	if set(0, `1`) == nil || set(999, `1`) == nil {
		t.Fatal("state without a row of this run")
	}
	if set(got[0].Row, `"`+strings.Repeat("x", 65536)+`"`) == nil {
		t.Fatal("unbounded state")
	}
}

func TestServiceDisableStopsTheRunAndEnableStartsIt(t *testing.T) {
	s := newService(t, false)
	s.fire("a")
	run, _ := s.run()
	if _, problem := s.SubscriptionSetEnabled(s.ctx, s.owner, contract.SubscriptionSetEnabledRequest{ID: s.sub, Enabled: false}); problem != nil {
		t.Fatal(problem)
	}
	if err := s.fireDue(s.ctx); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.store.WorkflowRun(s.ctx, run.ID)
	if err != nil || stopped.State != contract.WorkflowStopped {
		t.Fatalf("run %+v %v", stopped, err)
	}
	var waiting int
	if err := s.store.Read().QueryRowContext(s.ctx, "SELECT count(*) FROM workflow_queue WHERE sub=?", s.sub).Scan(&waiting); err != nil || waiting != 1 {
		t.Fatalf("events wait in the queue: %d %v", waiting, err)
	}
	if err := s.fireDue(s.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.ServiceRun(s.ctx, s.sub); err == nil {
		t.Fatal("a disabled service started")
	}
	if _, problem := s.SubscriptionSetEnabled(s.ctx, s.owner, contract.SubscriptionSetEnabledRequest{ID: s.sub, Enabled: true}); problem != nil {
		t.Fatal(problem)
	}
	next, box := s.run()
	if next.ID == run.ID {
		t.Fatal("enable did not start a new run")
	}
	if got := s.poll(box, "boot1", 10); len(got) != 1 {
		t.Fatalf("the waiting event after enable: %+v", got)
	}
}

func TestServiceRestartsAfterFailureWithBackoff(t *testing.T) {
	s := newService(t, false)
	fail := func(run store.WorkflowRun) {
		t.Helper()
		summary, err := s.workflowSummary(s.ctx, run, contract.WorkflowFailed)
		if err != nil {
			t.Fatal(err)
		}
		event, err := s.event(run.Room, engineSender, summary)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.EndWorkflow(s.ctx, run.ID, contract.WorkflowFailed, "boom", event); err != nil {
			t.Fatal(err)
		}
		sub, err := s.store.SubscriptionByID(s.ctx, s.sub)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.failService(s.ctx, sub, s.root, contract.CauseWorkflowFailed, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	started := func() bool {
		t.Helper()
		if err := s.fireDue(s.ctx); err != nil {
			t.Fatal(err)
		}
		_, err := s.store.ServiceRun(s.ctx, s.sub)
		return err == nil
	}
	run, _ := s.run()
	fail(run)
	for _, wait := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		s.clock.add(wait - time.Second)
		if started() {
			t.Fatalf("started before the %s backoff", wait)
		}
		s.clock.add(time.Second)
		if !started() {
			t.Fatalf("did not start after the %s backoff", wait)
		}
		run, _ = s.store.ServiceRun(s.ctx, s.sub)
		fail(run)
	}
	if s.notices("Workflow service") != 1 {
		t.Fatalf("the parent heard %d times, want once for the streak", s.notices("Workflow service"))
	}
	// A box that reaches serve ends the streak.
	s.clock.add(time.Hour)
	run, box := s.run()
	s.poll(box, "boot1", 10)
	stored, err := s.store.SubscriptionByID(s.ctx, s.sub)
	if err != nil || stored.Failures != 0 || !stored.HoldUntil.IsZero() {
		t.Fatalf("backoff after serving: %+v %v", stored, err)
	}
	_ = run
}

func TestServiceUpdateReloadsTheRunAndScriptsMustServe(t *testing.T) {
	s := newService(t, false)
	run, _ := s.run()
	changed := strings.Replace(serviceScript, "look", "look again", 1)
	if _, problem := s.SubscriptionUpdate(s.ctx, s.owner, contract.SubscriptionUpdateRequest{ID: s.sub, Workflow: &contract.WorkflowSubscription{Script: changed}}); problem != nil {
		t.Fatal(problem)
	}
	reloaded, err := s.store.WorkflowRun(s.ctx, run.ID)
	if err != nil || reloaded.Version != 2 || !strings.Contains(reloaded.Script, "look again") {
		t.Fatalf("reload %+v %v", reloaded, err)
	}
	if _, problem := s.SubscriptionUpdate(s.ctx, s.owner, contract.SubscriptionUpdateRequest{ID: s.sub, Workflow: &contract.WorkflowSubscription{Script: workflowScript}}); problem == nil || !strings.Contains(problem.Message, "serve(") {
		t.Fatalf("an update without serve: %v", problem)
	}
	_, problem := s.SubscriptionAdd(s.ctx, s.owner, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: s.room, On: []contract.EventType{"notice.posted"}, Workflow: &contract.WorkflowSubscription{Script: workflowScript}})
	if problem == nil || !strings.Contains(problem.Message, "serve(") {
		t.Fatalf("an add without serve: %v", problem)
	}
}

func TestServiceRecordsUsageForEachStepAndKeepsItWhenTheRowEnds(t *testing.T) {
	s := newService(t, false)
	s.fire("a")
	run, box := s.run()
	row := s.poll(box, "boot1", 10)[0].Row
	if _, problem := s.WorkflowStepStart(s.ctx, box, contract.WorkflowStepStartRequest{Key: "k1", Kind: contract.WorkflowStepAgent, Name: "review", Detail: "x", Event: row}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.WorkflowStepEnd(s.ctx, box, contract.WorkflowStepEndRequest{Key: "k1", State: contract.WorkflowStepDone, Result: `"ok"`, Usage: contract.Usage{InputTokens: 100, OutputTokens: 7}}); problem != nil {
		t.Fatal(problem)
	}
	status, problem := s.WorkflowStatus(s.ctx, s.owner, contract.WorkflowStatusRequest{Run: run.ID})
	if problem != nil || status.Run.Usage.InputTokens != 100 || len(status.Steps) != 1 {
		t.Fatalf("usage while the handler runs: %+v %v", status.Run.Usage, problem)
	}
	s.done(box, row, "")
	status, problem = s.WorkflowStatus(s.ctx, s.owner, contract.WorkflowStatusRequest{Run: run.ID})
	if problem != nil || status.Run.Usage.InputTokens != 100 || status.Run.Usage.OutputTokens != 7 || len(status.Steps) != 0 {
		t.Fatalf("usage after the ack: %+v steps %d %v", status.Run.Usage, len(status.Steps), problem)
	}
	list, problem := s.WorkflowList(s.ctx, s.owner, contract.WorkflowListRequest{Room: s.room})
	if problem != nil || list.Runs[0].Usage.InputTokens != 100 || list.Runs[0].Service == nil {
		t.Fatalf("list %+v %v", list.Runs, problem)
	}
}

func TestServiceStartRefusalBacksOffAndTellsTheParentOnce(t *testing.T) {
	s := newService(t, false)
	s.set(s.room, contract.WorkflowSettings{Start: contract.WorkflowStartOff})
	for range 3 {
		if err := s.fireDue(s.ctx); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := s.store.SubscriptionByID(s.ctx, s.sub)
	if err != nil || stored.Failures != 1 || !stored.HoldUntil.After(s.clock.now()) {
		t.Fatalf("backoff %+v %v", stored, err)
	}
	s.clock.add(time.Minute)
	if err := s.fireDue(s.ctx); err != nil {
		t.Fatal(err)
	}
	if failed := eventsOf[contract.HookFinished](t, s.Service, s.ctx, s.room); len(failed) != 2 || failed[1].Failures != 2 {
		t.Fatalf("refusals %+v", failed)
	}
	if s.notices("failed") != 1 {
		t.Fatal("the parent should hear once")
	}
}

func TestServiceHandsARowOverAgainWhenItsReplyWasLost(t *testing.T) {
	s := newService(t, false)
	s.fire("a")
	s.fire("a")
	_, box := s.run()
	lost := s.ask(box, "boot1", 10) // the engine restarted before the box read this
	again := s.poll(box, "boot1", 10)
	if len(lost) != 1 || len(again) != 1 || again[0].Row != lost[0].Row {
		t.Fatalf("the unread row, handed again: %+v then %+v", lost, again)
	}
	if more := s.poll(box, "boot1", 10); len(more) != 0 {
		t.Fatalf("a row the box holds, handed a third time: %+v", more)
	}
}

func TestServiceManualFirePayload(t *testing.T) {
	for _, data := range []string{`{"brief":"review","items":[1,true,null]}`, `null`, `42`, `"text"`, `[]`} {
		t.Run(data, func(t *testing.T) {
			s := newService(t, false)
			template := "{repo}#{number}"
			if _, problem := s.SubscriptionUpdate(s.ctx, s.owner, contract.SubscriptionUpdateRequest{ID: s.sub, Key: &template}); problem != nil {
				t.Fatal(problem)
			}
			if _, problem := s.SubscriptionFire(s.ctx, s.owner, contract.SubscriptionFireRequest{ID: s.sub, Data: json.RawMessage(data)}); problem != nil {
				t.Fatal(problem)
			}
			_, box := s.run()
			events := s.poll(box, "boot", 10)
			if len(events) != 1 || events[0].Key != "#0" {
				t.Fatalf("events: %+v", events)
			}
			var event contract.Event
			if err := json.Unmarshal(events[0].Event, &event); err != nil {
				t.Fatal(err)
			}
			body, ok := event.Body.(contract.HookFired)
			if !ok || body.Subscription != s.sub || string(body.Data) != data {
				t.Fatalf("handler body: %+v", event.Body)
			}
		})
	}
}

func TestServiceManualHookDueSubscription(t *testing.T) {
	for _, event := range []json.RawMessage{nil, json.RawMessage(`{"type":"hook.due","body":{}}`), json.RawMessage(`{"type":"hook.due","body":{"subscription":"wrong"}}`)} {
		s := newService(t, false)
		if _, problem := s.SubscriptionFire(s.ctx, s.owner, contract.SubscriptionFireRequest{ID: s.sub, Event: event}); problem != nil {
			t.Fatal(problem)
		}
		_, box := s.run()
		events := s.poll(box, "boot", 10)
		if len(events) != 1 {
			t.Fatalf("events: %+v", events)
		}
		var got contract.Event
		if err := json.Unmarshal(events[0].Event, &got); err != nil {
			t.Fatal(err)
		}
		if body, ok := got.Body.(contract.HookDue); !ok || body.Subscription != s.sub {
			t.Fatalf("handler body: %+v", got.Body)
		}
	}
}

func TestServiceManualFireRejectsInvalidData(t *testing.T) {
	s := newService(t, false)
	for _, req := range []contract.SubscriptionFireRequest{
		{Data: json.RawMessage(`null`), Event: json.RawMessage(`{"type":"hook.due","body":{}}`)},
		{Data: json.RawMessage(`{`)},
		{Data: json.RawMessage(`"` + strings.Repeat("a", 1<<20) + `"`)},
	} {
		req.ID = s.sub
		if _, problem := s.SubscriptionFire(s.ctx, s.owner, req); problem == nil {
			t.Fatalf("accepted invalid request")
		}
	}
}
