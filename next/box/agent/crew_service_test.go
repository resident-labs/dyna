package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// queueFake is the engine's queue of a service: rows wait, are handed to a box start, and leave when acknowledged.
type queueFake struct {
	mu        sync.Mutex
	waiting   []contract.WorkflowEvent
	handed    map[int64]string // the box start that holds each row
	handovers map[int64]int
	acked     map[int64]string // each acknowledged row to its error, in the order of acks
	order     []int64
	events    map[string]int64 // step key to the row of the handler that made it
	maxHand   int              // rows handed over more than this many times are dropped: 0 is three
	dropped   []int64
	polls     int
}

func (q *queueFake) stepEvent(key string, row int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.events == nil {
		q.events = map[string]int64{}
	}
	if row != 0 {
		q.events[key] = row
	}
}

func (q *queueFake) add(key string, body string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	row := int64(len(q.waiting) + len(q.acked) + len(q.handed) + 1)
	for _, r := range q.waiting {
		row = max(row, r.Row+1)
	}
	q.waiting = append(q.waiting, contract.WorkflowEvent{Row: row, Key: key, Event: json.RawMessage(body)})
	return row
}

// WorkflowEvents hands the oldest row of each key that no handler of this boot holds, as the engine does.
func (j *fakeJournal) WorkflowEvents(_ context.Context, req contract.WorkflowEventsRequest) (contract.WorkflowEventsResponse, error) {
	q := &j.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	q.polls++
	if q.handed == nil {
		q.handed, q.handovers, q.acked = map[int64]string{}, map[int64]int{}, map[int64]string{}
	}
	limit := q.maxHand
	if limit == 0 {
		limit = 3
	}
	var out []contract.WorkflowEvent
	seen := map[string]bool{}
	var keep []contract.WorkflowEvent
	for _, row := range q.waiting {
		if seen[row.Key] || len(out) >= req.Limit {
			keep = append(keep, row)
			seen[row.Key] = true
			continue
		}
		seen[row.Key] = true
		if q.handed[row.Row] == req.Boot && slices.Contains(req.Held, row.Row) {
			keep = append(keep, row)
			continue
		}
		if q.handovers[row.Row] >= limit {
			q.dropped = append(q.dropped, row.Row)
			seen[row.Key] = false
			continue
		}
		q.handovers[row.Row]++
		q.handed[row.Row] = req.Boot
		out = append(out, row)
		keep = append(keep, row)
	}
	q.waiting = keep
	return contract.WorkflowEventsResponse{Events: out}, nil
}

func (j *fakeJournal) WorkflowEventDone(_ context.Context, req contract.WorkflowEventDoneRequest) error {
	q := &j.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	q.waiting = slices.DeleteFunc(q.waiting, func(row contract.WorkflowEvent) bool { return row.Row == req.Row })
	q.acked[req.Row] = req.Error
	q.order = append(q.order, req.Row)
	j.mu.Lock()
	defer j.mu.Unlock()
	for key, row := range q.events {
		if row == req.Row {
			delete(j.steps, key)
		}
	}
	return nil
}

func (q *queueFake) ackedRows() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.order)
}

func (q *queueFake) waitAcked(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for len(q.ackedRows()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("acked %v, want %d", q.ackedRows(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func serviceHarness(t *testing.T) *crewHarness {
	h := newCrewHarness(t)
	h.workflow = &contract.BoxWorkflow{Run: "wf_1", Subscription: "sub_1", Name: "test", Version: 1, Profiles: crewProfiles, Workers: 16}
	return h
}

func stopped(t *testing.T, crew *Crew, ended <-chan contract.TurnEnded) {
	t.Helper()
	_ = crew.Abort(context.Background())
	select {
	case out := <-ended:
		if out.Outcome != contract.TurnStopped {
			t.Fatalf("turn %+v", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the service did not stop")
	}
}

func TestServiceHandlesKeysConcurrentlyAndInOrderPerKey(t *testing.T) {
	h := serviceHarness(t)
	var mu sync.Mutex
	var log []string
	gate := make(chan struct{})
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		mu.Lock()
		log = append(log, "start "+prompt)
		mu.Unlock()
		if prompt == "a1" {
			<-gate // b1 must finish while a1 is still in its handler
		}
		mu.Lock()
		log = append(log, "end "+prompt)
		mu.Unlock()
		return contract.TurnEnded{Final: "ok"}, nil
	}
	q := &h.journal.queue
	q.add("a", `{"n":"a1"}`)
	q.add("a", `{"n":"a2"}`)
	q.add("b", `{"n":"b1"}`)
	crew, ended := h.start(crewMeta + "serve(async (event, key) => { await agent(event.n, { lane: key }) })")
	deadline := time.Now().Add(10 * time.Second)
	for len(q.ackedRows()) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rows := q.ackedRows(); len(rows) != 1 || rows[0] != 3 {
		t.Fatalf("b1 should finish first, while a1 is held: %v", rows)
	}
	close(gate)
	q.waitAcked(t, 3)
	stopped(t, crew, ended)
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(log, ",")
	if strings.Index(joined, "end a1") > strings.Index(joined, "start a2") {
		t.Fatalf("a2 started before a1 ended: %s", joined)
	}
}

func TestServiceAcknowledgesAfterTheHandlerAndKeepsErrors(t *testing.T) {
	h := serviceHarness(t)
	q := &h.journal.queue
	q.add("k", `{"fail":true}`)
	q.add("j", `{"fail":false}`)
	crew, ended := h.start(crewMeta + "serve(async (event) => { await agent('x'); if (event.fail) throw new Error('boom') })")
	q.waitAcked(t, 2)
	stopped(t, crew, ended)
	if q.acked[1] != "boom" || q.acked[2] != "" {
		t.Fatalf("acks %v", q.acked)
	}
	for key := range q.events {
		if _, ok := h.journal.steps[key]; ok {
			t.Fatal("an acknowledged row's steps stay in the journal")
		}
	}
}

func TestServiceRedeliversAfterABoxRestartAndHitsFinishedSteps(t *testing.T) {
	h := serviceHarness(t)
	q := &h.journal.queue
	q.add("k", `{}`)
	var workers int
	var mu sync.Mutex
	var dying atomic.Bool
	dying.Store(true)
	h.hold = func(ctx context.Context, prompt string) error {
		mu.Lock()
		workers++
		mu.Unlock()
		if prompt == "second" && dying.Load() {
			<-ctx.Done() // the first box dies here
			return ctx.Err()
		}
		return nil
	}
	script := crewMeta + "serve(async () => { await agent('first'); await agent('second') })"
	crew, ended := h.start(script)
	deadline := time.Now().Add(10 * time.Second)
	for h.started.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(q.ackedRows()) != 0 {
		t.Fatal("acknowledged before the handler ended")
	}
	_ = crew.Abort(context.Background())
	<-ended
	dying.Store(false)
	// A new box start: a new Crew on the same journal.
	crew, ended = h.start(script)
	q.waitAcked(t, 1)
	stopped(t, crew, ended)
	if q.handovers[1] != 2 {
		t.Fatalf("hand-overs %d", q.handovers[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if workers != 3 { // first once, second in both boxes: the finished step was a hit
		t.Fatalf("workers %d, want 3", workers)
	}
}

func TestServiceDropsARowAfterThreeHandOvers(t *testing.T) {
	h := serviceHarness(t)
	q := &h.journal.queue
	q.add("k", `{}`)
	script := crewMeta + "serve(async () => { await agent('never') })"
	h.hold = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	for range 4 {
		crew, ended := h.start(script)
		time.Sleep(300 * time.Millisecond)
		_ = crew.Abort(context.Background())
		<-ended
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !slices.Equal(q.dropped, []int64{1}) || q.handovers[1] != 3 {
		t.Fatalf("dropped %v after %d hand-overs", q.dropped, q.handovers[1])
	}
}

func TestServiceBodyRunsOnEachNewBox(t *testing.T) {
	h := serviceHarness(t)
	script := crewMeta + "await run('echo setup'); serve(async () => {})"
	for i := range 2 {
		crew, ended := h.start(script)
		deadline := time.Now().Add(10 * time.Second)
		for h.journal.queue.pollCount() <= i && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		stopped(t, crew, ended)
	}
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	n := 0
	for _, name := range h.journal.started {
		if strings.Contains(name, "echo setup") {
			n++
		}
	}
	if len(h.journal.steps) != 2 {
		t.Fatalf("body steps stored %d, want one for each box start", len(h.journal.steps))
	}
	_ = n
}

func (q *queueFake) pollCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.polls
}

func TestServiceRefusesABodyThatEndsWithoutServe(t *testing.T) {
	h := serviceHarness(t)
	ended := h.run(crewMeta + "return 1")
	if ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "serve()") {
		t.Fatalf("turn %+v", ended)
	}
}

func TestServeOutsideASubscriptionIsRefused(t *testing.T) {
	h := newCrewHarness(t)
	ended := h.run(crewMeta + "serve(async () => {})")
	if ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "needs a subscription") {
		t.Fatalf("turn %+v", ended)
	}
}

func TestServiceReloadRunsAHandlerAgainWithTheNewVersion(t *testing.T) {
	h := serviceHarness(t)
	q := &h.journal.queue
	q.add("k", `{}`)
	release := make(chan struct{})
	h.reply = func(prompt, _ string) (contract.TurnEnded, error) {
		if prompt == "slow" {
			<-release
		}
		return contract.TurnEnded{Final: prompt}, nil
	}
	crew, ended := h.start(crewMeta + "serve(async () => { await agent('slow'); await agent('v1') })")
	deadline := time.Now().Add(10 * time.Second)
	for h.started.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	crew.Reload(contract.BoxWorkflow{Run: "wf_1", Subscription: "sub_1", Version: 2, Script: crewMeta + "serve(async () => { await agent('slow'); await agent('v2') })", Profiles: crewProfiles, Workers: 16})
	close(release)
	q.waitAcked(t, 1)
	stopped(t, crew, ended)
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	if !slices.Contains(h.journal.started, "agent: v2") || len(q.ackedRows()) != 1 {
		t.Fatalf("started %v", h.journal.started)
	}
	_ = fmt.Sprint()
}

func TestServiceSetupAfterServeStaysUnderTheBoxStart(t *testing.T) {
	h := serviceHarness(t)
	q := &h.journal.queue
	q.add("k", `{}`)
	crew, ended := h.start(crewMeta + "serve(async () => { await agent('handle') }); await run('echo after')")
	q.waitAcked(t, 1)
	stopped(t, crew, ended)
	q.mu.Lock()
	defer q.mu.Unlock()
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	for key := range q.events {
		if name := h.journal.names[key]; !strings.HasPrefix(name, "agent") {
			t.Fatalf("setup after serve ran as the event's step %q", name)
		}
	}
}
