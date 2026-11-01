package engine

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func sessionCaller(id contract.SessionID) contract.Principal {
	return contract.Principal{Kind: contract.PrincipalSession, Session: id}
}

// fakeRuns stands in for hosts. A Run stays running until the test sets its status.
type fakeRuns struct {
	mu       sync.Mutex
	started  []box.RunSpec
	restored []box.RunSpec
	removed  []contract.BoxID
	status   map[contract.BoxID]box.RunStatus
	statusEr error
	output   string
	startEr  error
}

func (f *fakeRuns) StartRun(_ context.Context, spec box.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	return f.startEr
}
func (f *fakeRuns) RestoreRun(_ context.Context, spec box.RunSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, spec)
	return nil
}
func (f *fakeRuns) RunStatus(_ context.Context, _ contract.HostName, id contract.BoxID) (box.RunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusEr != nil {
		return box.RunStatus{}, f.statusEr
	}
	if status, ok := f.status[id]; ok {
		return status, nil
	}
	return box.RunStatus{Found: true, Running: true}, nil
}
func (f *fakeRuns) RunOutput(context.Context, contract.HostName, contract.BoxID) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte(f.output), nil
}
func (f *fakeRuns) RemoveRun(_ context.Context, _ contract.HostName, id contract.BoxID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeRuns) exit(id contract.BoxID, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == nil {
		f.status = make(map[contract.BoxID]box.RunStatus)
	}
	f.status[id] = box.RunStatus{Found: true, ExitCode: code}
}
func (f *fakeRuns) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}
func (f *fakeRuns) last() box.RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[len(f.started)-1]
}

// clock is a test's time, moved by hand.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func withRuns(t *testing.T, s *Service) (*fakeRuns, *clock) {
	t.Helper()
	runs := &fakeRuns{}
	c := &clock{at: s.now()}
	s.now = c.now
	s.SetHookRunner(runs, nil)
	return runs, c
}

func pass(t *testing.T, s *Service, ctx context.Context) {
	t.Helper()
	if err := s.subscriptionPass(ctx, true); err != nil {
		t.Fatal(err)
	}
	s.hookStarts.group.Wait()
}

func runRequest(room contract.RoomID) contract.SubscriptionAddRequest {
	return contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: contract.DurationMS((30 * time.Minute).Milliseconds()),
		Run: &contract.RunSpec{Command: []string{"sh", "watch.sh"}, Image: "base", Size: contract.BoxSizeSmall, Files: []contract.RunFile{{Path: "watch.sh", Body: "echo hi"}}}}
}

func addSubscription(t *testing.T, s *Service, ctx context.Context, req contract.SubscriptionAddRequest) contract.SubscriptionID {
	t.Helper()
	r, problem := s.SubscriptionAdd(ctx, ownerCaller, req)
	if problem != nil {
		t.Fatal(problem)
	}
	return r.ID
}

func eventsOf[T contract.EventBody](t *testing.T, s *Service, ctx context.Context, room contract.RoomID) []T {
	t.Helper()
	var zero T
	events, err := s.store.Events(ctx, room, 0, 1000, []contract.EventType{zero.EventType()})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]T, 0, len(events))
	for _, event := range events {
		out = append(out, event.Body.(T))
	}
	return out
}

func pendingWakes(t *testing.T, s *Service, ctx context.Context, session contract.SessionID) int {
	t.Helper()
	var n int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries WHERE to_id=? AND wake=1 AND state='pending'", session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func listed(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, id contract.SubscriptionID) contract.SubscriptionInfo {
	t.Helper()
	r, problem := s.SubscriptionList(ctx, ownerCaller, contract.SubscriptionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, info := range r.Subscriptions {
		if info.Subscription.ID == id {
			return info
		}
	}
	t.Fatalf("subscription %s is not listed", id)
	return contract.SubscriptionInfo{}
}

func TestSubscriptionShapeRefusals(t *testing.T) {
	s, room, worker, ctx := setup(t)
	wake := &contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	every := contract.DurationMS(time.Hour.Milliseconds())
	run := &contract.RunSpec{Command: []string{"true"}, Image: "base", Size: contract.BoxSizeSmall}
	base := contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room}
	for name, change := range map[string]func(*contract.SubscriptionAddRequest){
		"both triggers": func(r *contract.SubscriptionAddRequest) {
			r.Every, r.On, r.Wake = every, []contract.EventType{"post.created"}, wake
		},
		"no trigger":    func(r *contract.SubscriptionAddRequest) { r.Wake = wake },
		"both actions":  func(r *contract.SubscriptionAddRequest) { r.Every, r.Wake, r.Run = every, wake, run },
		"no action":     func(r *contract.SubscriptionAddRequest) { r.Every = every },
		"too often":     func(r *contract.SubscriptionAddRequest) { r.Every, r.Wake = 1000, wake },
		"unknown event": func(r *contract.SubscriptionAddRequest) { r.On, r.Wake = []contract.EventType{"nope.done"}, wake },
		"live event":    func(r *contract.SubscriptionAddRequest) { r.On, r.Wake = []contract.EventType{"turn.activity"}, wake },
		"run on a hook": func(r *contract.SubscriptionAddRequest) { r.On, r.Run = []contract.EventType{"hook.finished"}, run },
		"large box": func(r *contract.SubscriptionAddRequest) {
			large := *run
			large.Size = contract.BoxSizeLarge
			r.Every, r.Run = every, &large
		},
		"registry image": func(r *contract.SubscriptionAddRequest) {
			other := *run
			other.Image = "docker.io/evil:1"
			r.Every, r.Run = every, &other
		},
		"escaping file": func(r *contract.SubscriptionAddRequest) {
			other := *run
			other.Files = []contract.RunFile{{Path: "../../etc/x", Body: "x"}}
			r.Every, r.Run = every, &other
		},
		"absolute file": func(r *contract.SubscriptionAddRequest) {
			other := *run
			other.Files = []contract.RunFile{{Path: "/etc/x", Body: "x"}}
			r.Every, r.Run = every, &other
		},
		"no command": func(r *contract.SubscriptionAddRequest) {
			other := *run
			other.Command = nil
			r.Every, r.Run = every, &other
		},
		"bad scope": func(r *contract.SubscriptionAddRequest) { r.Scope, r.Every, r.Wake = "planet", every, wake },
	} {
		req := base
		change(&req)
		if _, problem := s.SubscriptionAdd(ctx, ownerCaller, req); problem == nil || problem.Code != contract.ErrInvalid {
			t.Errorf("%s: got %v", name, problem)
		}
	}
}

func TestSubscriptionAuthority(t *testing.T) {
	s, room, worker, ctx := setup(t)
	root, err := s.store.RootBoxHost(ctx, room)
	if err != nil || root == "" {
		t.Fatalf("root box host: %q %v", root, err)
	}
	var rootID contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	child, problem := s.SessionStart(ctx, ownerCaller, contract.SessionStartRequest{Room: room, Parent: worker, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	other, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	every := contract.DurationMS(time.Hour.Milliseconds())
	wakeOf := func(id contract.SessionID) *contract.Principal {
		return &contract.Principal{Kind: contract.PrincipalSession, Session: id}
	}
	scoped := func(id contract.SessionID, wake *contract.Principal) contract.SubscriptionAddRequest {
		return contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: id, Every: every, Wake: wake}
	}
	machine := contract.SubscriptionAddRequest{Scope: contract.ScopeMachine, Every: every, Wake: wakeOf(worker)}
	roomWake := contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: every, Wake: wakeOf(worker)}
	for name, c := range map[string]struct {
		caller contract.Principal
		req    contract.SubscriptionAddRequest
		code   contract.ErrorCode
	}{
		"session on the machine":        {sessionCaller(rootID), machine, contract.ErrForbidden},
		"non-root on the room":          {sessionCaller(worker), roomWake, contract.ErrForbidden},
		"non-root adds a run":           {sessionCaller(worker), runRequest(room), contract.ErrForbidden},
		"non-root adds a session run":   {sessionCaller(worker), contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: worker, Every: every, Run: runRequest(room).Run}, contract.ErrForbidden},
		"child wakes its parent":        {sessionCaller(child.Session), scoped(child.Session, wakeOf(worker)), contract.ErrForbidden},
		"child owns its parent's scope": {sessionCaller(child.Session), scoped(worker, wakeOf(child.Session)), contract.ErrForbidden},
		"root of another room":          {sessionCaller(rootID), contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: other.Room, Every: every, Run: runRequest(room).Run}, contract.ErrForbidden},
		"wake across rooms":             {ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: other.Room, Every: every, Wake: wakeOf(worker)}, contract.ErrInvalid},
	} {
		if _, problem := s.SubscriptionAdd(ctx, c.caller, c.req); problem == nil || problem.Code != c.code {
			t.Errorf("%s: got %v, want %s", name, problem, c.code)
		}
	}
	for name, c := range map[string]struct {
		caller contract.Principal
		req    contract.SubscriptionAddRequest
	}{
		"session on itself":     {sessionCaller(worker), scoped(worker, wakeOf(worker))},
		"ancestor on its child": {sessionCaller(worker), scoped(child.Session, wakeOf(child.Session))},
		"root on the room":      {sessionCaller(rootID), roomWake},
		"root adds a run":       {sessionCaller(rootID), runRequest(room)},
		"owner on the machine":  {ownerCaller, machine},
	} {
		if _, problem := s.SubscriptionAdd(ctx, c.caller, c.req); problem != nil {
			t.Errorf("%s: %v", name, problem)
		}
	}
	// A subscription stays under the same authority after it exists, and a session cannot read another room's.
	id := addSubscription(t, s, ctx, runRequest(room))
	if _, problem := s.SubscriptionRemove(ctx, sessionCaller(worker), contract.SubscriptionRemoveRequest{ID: id}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Errorf("worker removed a run: %v", problem)
	}
	if _, problem := s.SubscriptionSetEnabled(ctx, sessionCaller(child.Session), contract.SubscriptionSetEnabledRequest{ID: id, Enabled: false}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Errorf("child disabled a run: %v", problem)
	}
	if _, problem := s.SubscriptionList(ctx, sessionCaller(worker), contract.SubscriptionListRequest{Room: other.Room}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Errorf("worker listed another room: %v", problem)
	}
	own, problem := s.SubscriptionList(ctx, sessionCaller(worker), contract.SubscriptionListRequest{})
	if problem != nil || len(own.Subscriptions) == 0 {
		t.Errorf("worker lists its room: %v %v", own, problem)
	}
	for _, info := range own.Subscriptions {
		if info.Subscription.Scope == contract.ScopeMachine {
			t.Error("a session saw a machine subscription")
		}
	}
	// The default subscription stays, and only the owner turns it off.
	def := store.DefaultSubscriptionID(room)
	if _, problem := s.SubscriptionRemove(ctx, ownerCaller, contract.SubscriptionRemoveRequest{ID: def}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Errorf("default removed: %v", problem)
	}
	if _, problem := s.SubscriptionSetEnabled(ctx, sessionCaller(rootID), contract.SubscriptionSetEnabledRequest{ID: def, Enabled: false}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Errorf("root turned off its default: %v", problem)
	}
	if _, problem := s.SubscriptionSetEnabled(ctx, ownerCaller, contract.SubscriptionSetEnabledRequest{ID: def, Enabled: false}); problem != nil {
		t.Errorf("owner turns off the default: %v", problem)
	}
}

func recordNotice(t *testing.T, s *Service, ctx context.Context, room contract.RoomID) {
	t.Helper()
	event, err := s.event(room, engineSender, contract.NoticePosted{Text: "disk is low"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.RecordEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultSubscriptionWakesRoot(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	info := listed(t, s, ctx, room, store.DefaultSubscriptionID(room))
	if !info.Default || info.Subscription.Wake == nil || info.Subscription.Wake.Session != root {
		t.Fatalf("default subscription: %+v", info)
	}
	before := pendingWakes(t, s, ctx, root)
	recordNotice(t, s, ctx, room)
	pass(t, s, ctx)
	if got := pendingWakes(t, s, ctx, root); got != before+1 {
		t.Fatalf("notice: wakes %d then %d", before, got)
	}
	// A second pass repeats nothing.
	pass(t, s, ctx)
	if got := pendingWakes(t, s, ctx, root); got != before+1 {
		t.Fatalf("dispatch repeated: wakes %d", got)
	}
	// An owned repository event stays with its owner. One with no owner reaches root.
	for _, body := range []contract.PrOpened{{Repo: "o/r", Number: 1, Session: "ses_someone"}, {Repo: "o/r", Number: 2}} {
		event, err := s.event(room, engineSender, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.RecordEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	pass(t, s, ctx)
	if got := pendingWakes(t, s, ctx, root); got != before+2 {
		t.Fatalf("repository events: wakes %d, want %d", got, before+2)
	}
}

func TestEventWakeSkipsOwnEventsAndDMs(t *testing.T) {
	s, room, worker, ctx := setup(t)
	addSubscription(t, s, ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: worker, On: []contract.EventType{"post.created"}, Wake: &contract.Principal{Kind: contract.PrincipalSession, Session: worker}})
	pass(t, s, ctx)
	var delivered func() int
	delivered = func() int {
		var n int
		if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='post.created'", worker).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := delivered()
	if _, problem := s.Post(ctx, sessionCaller(worker), contract.PostRequest{Room: room, Text: "mine"}); problem != nil {
		t.Fatal(problem)
	}
	var rootID contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.Post(ctx, ownerCaller, contract.PostRequest{Room: room, Text: "for root only", Home: contract.Conversation{Kind: contract.ConversationDM, Session: rootID}}); problem != nil {
		t.Fatal(problem)
	}
	pass(t, s, ctx)
	if got := delivered(); got != before {
		t.Fatalf("own post or a DM reached the subscriber: %d then %d", before, got)
	}
	if _, problem := s.Post(ctx, sessionCaller(rootID), contract.PostRequest{Room: room, Text: "for everyone"}); problem != nil {
		t.Fatal(problem)
	}
	pass(t, s, ctx)
	if got := delivered(); got != before+1 {
		t.Fatalf("another session's post: %d then %d", before, got)
	}
}

func TestEventRunCoalescesAndWaits(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	req := runRequest(room)
	req.Every, req.On = 0, []contract.EventType{"notice.posted"}
	id := addSubscription(t, s, ctx, req)
	pass(t, s, ctx)
	if runs.count() != 0 {
		t.Fatal("an event subscription ran with no event")
	}
	recordNotice(t, s, ctx, room)
	recordNotice(t, s, ctx, room)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("two events: %d runs", runs.count())
	}
	// An event during the Run makes one more Run after it, never one beside it.
	recordNotice(t, s, ctx, room)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("overlap: %d runs", runs.count())
	}
	runs.exit(runs.last().ID, 0)
	c.add(time.Second)
	pass(t, s, ctx)
	pass(t, s, ctx)
	if runs.count() != 2 {
		t.Fatalf("after the first Run: %d runs", runs.count())
	}
	if got := listed(t, s, ctx, room, id); !got.Running {
		t.Fatalf("list: %+v", got)
	}
}

func TestScheduleDoesNotOverlap(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	req := runRequest(room)
	req.Every = contract.DurationMS((10 * time.Minute).Milliseconds())
	addSubscription(t, s, ctx, req)
	pass(t, s, ctx)
	if runs.count() != 0 {
		t.Fatal("a schedule fired before its first period")
	}
	c.add(10 * time.Minute)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("first period: %d runs", runs.count())
	}
	first := runs.last()
	if first.Host != "host" || first.Image != "base" || first.Size.Name != contract.BoxSizeSmall || first.Room != room || len(first.Files) != 1 {
		t.Fatalf("run spec: %+v", first)
	}
	// Three periods pass with the first Run still going: nothing stacks up.
	for range 3 {
		c.add(10 * time.Minute)
		pass(t, s, ctx)
	}
	if runs.count() != 1 {
		t.Fatalf("overlapping runs: %d", runs.count())
	}
	runs.mu.Lock()
	runs.output = "all quiet"
	runs.mu.Unlock()
	runs.exit(first.ID, 0)
	pass(t, s, ctx)
	done := eventsOf[contract.HookFinished](t, s, ctx, room)
	if len(done) != 1 || done[0].Result != contract.HookPassed || done[0].Output != "all quiet" {
		t.Fatalf("hook.finished: %+v", done)
	}
	// What was missed fires once, not three times.
	pass(t, s, ctx)
	if runs.count() != 2 {
		t.Fatalf("after the overrun: %d runs", runs.count())
	}
	pass(t, s, ctx)
	if runs.count() != 2 {
		t.Fatalf("stacked after the overrun: %d runs", runs.count())
	}
}

func TestScheduleSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	c := &clock{at: time.Unix(1000, 0)}
	open := func() *Service {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s := New(st, fakeBoxes{}, fakeProfiles{}, c.now)
		resetLifecycle(s)
		return s
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := open()
	if err := s.store.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	made, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	runs := &fakeRuns{}
	s.SetHookRunner(runs, nil)
	id := addSubscription(t, s, ctx, runRequest(made.Room))
	c.add(20 * time.Minute)
	pass(t, s, ctx)
	due := listed(t, s, ctx, made.Room, id).NextAt
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}

	// The engine is down past the due time. It comes back with the same due time and fires once.
	c.add(3 * time.Hour)
	s = open()
	defer s.store.Close()
	s.SetHookRunner(runs, nil)
	info := listed(t, s, ctx, made.Room, id)
	if info.NextAt == nil || !info.NextAt.Equal(*due) {
		t.Fatalf("schedule moved across a restart: %v then %v", due, info.NextAt)
	}
	pass(t, s, ctx)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("after a restart: %d runs", runs.count())
	}
	next := listed(t, s, ctx, made.Room, id).NextAt
	if want := c.now().Add(30 * time.Minute); next == nil || !next.Equal(want) {
		t.Fatalf("next firing %v, want %v", next, want)
	}
}

func TestRunOutlivesTheEngine(t *testing.T) {
	dir := t.TempDir()
	c := &clock{at: time.Unix(1000, 0)}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(st, fakeBoxes{}, fakeProfiles{}, c.now)
	resetLifecycle(s)
	if err := st.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	made, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	runs := &fakeRuns{}
	s.SetHookRunner(runs, nil)
	addSubscription(t, s, ctx, runRequest(made.Room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	run := runs.last()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = New(st, fakeBoxes{}, fakeProfiles{}, c.now)
	resetLifecycle(s)
	s.SetHookRunner(runs, nil)
	if err := s.restoreRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runs.restored) != 1 || runs.restored[0].ID != run.ID || runs.restored[0].Room != made.Room {
		t.Fatalf("restored: %+v", runs.restored)
	}
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("a restart started a second Run: %d", runs.count())
	}
	runs.exit(run.ID, 0)
	pass(t, s, ctx)
	if done := eventsOf[contract.HookFinished](t, s, ctx, made.Room); len(done) != 1 || done[0].Result != contract.HookPassed {
		t.Fatalf("hook.finished: %+v", done)
	}
}

func TestFailingRunBacksOffAndShows(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	runs, c := withRuns(t, s)
	id := addSubscription(t, s, ctx, runRequest(room))
	period := 30 * time.Minute
	fail := func() {
		t.Helper()
		pass(t, s, ctx)
		runs.exit(runs.last().ID, 3)
		pass(t, s, ctx)
	}
	c.add(period)
	before := pendingWakes(t, s, ctx, root)
	fail()
	info := listed(t, s, ctx, room, id)
	if info.Failures != 1 || info.Last != contract.HookFailed || info.NextAt == nil {
		t.Fatalf("first failure: %+v", info)
	}
	if got := len(eventsOf[contract.NoticePosted](t, s, ctx, room)); got != 1 {
		t.Fatalf("first failure: %d notices", got)
	}
	pass(t, s, ctx)
	if got := pendingWakes(t, s, ctx, root); got != before+1 {
		t.Fatalf("a failing Run did not wake root: %d then %d", before, got)
	}
	// One period holds it; the failure doubles the wait from there.
	started := runs.count()
	c.add(period - time.Second)
	pass(t, s, ctx)
	if runs.count() != started {
		t.Fatal("ran inside the backoff")
	}
	c.add(time.Second)
	fail()
	if runs.count() != started+1 {
		t.Fatalf("after the backoff: %d runs, want %d", runs.count(), started+1)
	}
	info = listed(t, s, ctx, room, id)
	if info.Failures != 2 || !info.NextAt.Equal(c.now().Add(2*period)) {
		t.Fatalf("second failure: %+v, now %v", info, c.now())
	}
	if got := len(eventsOf[contract.NoticePosted](t, s, ctx, room)); got != 1 {
		t.Fatalf("a repeated failure posted again: %d notices", got)
	}
	failed := eventsOf[contract.HookFinished](t, s, ctx, room)
	if last := failed[len(failed)-1]; last.Result != contract.HookFailed || last.ExitCode != 3 || last.Failures != 2 || last.RetryAt == nil {
		t.Fatalf("hook.finished: %+v", last)
	}
	// Recovery clears the count, so the next failure reports again.
	c.add(2 * period)
	pass(t, s, ctx)
	runs.exit(runs.last().ID, 0)
	pass(t, s, ctx)
	if info = listed(t, s, ctx, room, id); info.Failures != 0 || info.Last != contract.HookPassed {
		t.Fatalf("after recovery: %+v", info)
	}
	c.add(period)
	fail()
	if got := len(eventsOf[contract.NoticePosted](t, s, ctx, room)); got != 2 {
		t.Fatalf("failing anew: %d notices", got)
	}
}

func TestStartFailureCountsAsAFailure(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	runs.startEr = errors.New("no such image")
	id := addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	done := eventsOf[contract.HookFinished](t, s, ctx, room)
	if len(done) != 1 || done[0].Cause != contract.CauseStartFailed || !strings.Contains(done[0].Output, "no such image") {
		t.Fatalf("hook.finished: %+v", done)
	}
	if info := listed(t, s, ctx, room, id); info.Failures != 1 || info.Running {
		t.Fatalf("list: %+v", info)
	}
}

func TestBackoffCaps(t *testing.T) {
	if got := backoff(30*time.Minute, 1); got != 30*time.Minute {
		t.Errorf("first: %v", got)
	}
	if got := backoff(0, 2); got != 2*time.Minute {
		t.Errorf("event subscription: %v", got)
	}
	if got := backoff(30*time.Minute, 500); got != backoffCap {
		t.Errorf("cap: %v", got)
	}
}

func TestPausedRoomDoesNotFire(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	runs, c := withRuns(t, s)
	id := addSubscription(t, s, ctx, runRequest(room))
	wake := addSubscription(t, s, ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: contract.DurationMS(time.Hour.Milliseconds()), Wake: &contract.Principal{Kind: contract.PrincipalSession, Session: root}})
	if _, problem := s.RoomPause(ctx, ownerCaller, contract.RoomPauseRequest{Room: room, Paused: true}); problem != nil {
		t.Fatal(problem)
	}
	before := pendingWakes(t, s, ctx, root)
	c.add(5 * time.Hour)
	recordNotice(t, s, ctx, room)
	pass(t, s, ctx)
	pass(t, s, ctx)
	if runs.count() != 0 {
		t.Fatalf("a paused room ran: %d", runs.count())
	}
	if got := pendingWakes(t, s, ctx, root); got != before {
		t.Fatalf("a paused room woke root: %d then %d", before, got)
	}
	if got := eventsOf[contract.HookDue](t, s, ctx, room); len(got) != 0 {
		t.Fatalf("a paused room fired a wake: %+v", got)
	}
	if info := listed(t, s, ctx, room, id); info.NextAt == nil || info.Running {
		t.Fatalf("list in a pause: %+v", info)
	}
	// Resuming fires what was due, once.
	if _, problem := s.RoomPause(ctx, ownerCaller, contract.RoomPauseRequest{Room: room, Paused: false}); problem != nil {
		t.Fatal(problem)
	}
	pass(t, s, ctx)
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("after resuming: %d runs", runs.count())
	}
	if got := eventsOf[contract.HookDue](t, s, ctx, room); len(got) != 1 || got[0].Subscription != wake {
		t.Fatalf("after resuming: %+v", got)
	}
}

func TestScheduledWakeDoesNotStack(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	_, c := withRuns(t, s)
	addSubscription(t, s, ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: contract.DurationMS(time.Hour.Milliseconds()), Wake: &contract.Principal{Kind: contract.PrincipalSession, Session: root}})
	before := pendingWakes(t, s, ctx, root)
	for range 4 {
		c.add(time.Hour)
		pass(t, s, ctx)
	}
	if got := pendingWakes(t, s, ctx, root); got != before+1 {
		t.Fatalf("wakes stacked up: %d then %d", before, got)
	}
}

func TestDisabledSubscriptionDoesNotFire(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	id := addSubscription(t, s, ctx, runRequest(room))
	if _, problem := s.SubscriptionSetEnabled(ctx, ownerCaller, contract.SubscriptionSetEnabledRequest{ID: id, Enabled: false}); problem != nil {
		t.Fatal(problem)
	}
	c.add(time.Hour)
	pass(t, s, ctx)
	if runs.count() != 0 {
		t.Fatal("a disabled subscription ran")
	}
	if info := listed(t, s, ctx, room, id); !info.Disabled || info.NextAt != nil {
		t.Fatalf("list: %+v", info)
	}
	if _, problem := s.SubscriptionSetEnabled(ctx, ownerCaller, contract.SubscriptionSetEnabledRequest{ID: id, Enabled: true}); problem != nil {
		t.Fatal(problem)
	}
	pass(t, s, ctx)
	if runs.count() != 1 {
		t.Fatalf("after enabling: %d runs", runs.count())
	}
}

func TestRemovingASubscriptionStopsItsRun(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	id := addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	run := runs.last()
	if _, problem := s.SubscriptionRemove(ctx, ownerCaller, contract.SubscriptionRemoveRequest{ID: id}); problem != nil {
		t.Fatal(problem)
	}
	pass(t, s, ctx)
	if len(runs.removed) != 1 || runs.removed[0] != run.ID {
		t.Fatalf("removed: %v", runs.removed)
	}
	if done := eventsOf[contract.HookFinished](t, s, ctx, room); len(done) != 1 || done[0].Result != contract.HookStopped {
		t.Fatalf("hook.finished: %+v", done)
	}
	active, err := s.store.ActiveRuns(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active: %+v %v", active, err)
	}
}

func TestRunPastItsLimitIsKilled(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	c.add(box.RunLimit + runGrace)
	if err := s.watchRuns(ctx); err != nil {
		t.Fatal(err)
	}
	done := eventsOf[contract.HookFinished](t, s, ctx, room)
	if len(done) != 1 || done[0].Cause != contract.CauseRunTimeout || done[0].Result != contract.HookFailed {
		t.Fatalf("hook.finished: %+v", done)
	}
	if len(runs.removed) != 1 {
		t.Fatalf("removed: %v", runs.removed)
	}
}

func TestLostHostEndsARun(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	runs.mu.Lock()
	runs.statusEr = errors.New("ssh: no route")
	runs.mu.Unlock()
	// A host that stops answering holds the Run, so a brief outage starts nothing beside it.
	if err := s.watchRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if done := eventsOf[contract.HookFinished](t, s, ctx, room); len(done) != 0 {
		t.Fatalf("finished during an outage: %+v", done)
	}
	c.add(box.RunLimit + runGrace)
	if err := s.watchRuns(ctx); err != nil {
		t.Fatal(err)
	}
	done := eventsOf[contract.HookFinished](t, s, ctx, room)
	if len(done) != 1 || done[0].Cause != contract.CauseHostLost {
		t.Fatalf("hook.finished: %+v", done)
	}
}

func TestPlacementFollowsTheSharedFolder(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	if err := s.store.AddHost(ctx, box.Host{Name: "bigger", State: box.HostReady, CPUs: 64, MemBytes: 256 * box.GiB, DiskBytes: 400 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	if runs.count() != 1 || runs.last().Host != "host" {
		t.Fatalf("a Run left the host of the room's root: %+v", runs.started)
	}
}

func TestRoomLimitHoldsARun(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	if _, problem := s.RoomSetLimits(ctx, ownerCaller, contract.RoomSetLimitsRequest{Room: room, CPUs: 1, MemBytes: box.GiB}); problem != nil {
		t.Fatal(problem)
	}
	id := addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	if runs.count() != 0 {
		t.Fatalf("over the room limit: %d runs", runs.count())
	}
	if info := listed(t, s, ctx, room, id); info.Failures != 0 || info.NextAt == nil {
		t.Fatalf("a waiting Run is no failure: %+v", info)
	}
}

func TestSessionCloseEndsItsSubscriptions(t *testing.T) {
	s, room, worker, ctx := setup(t)
	id := addSubscription(t, s, ctx, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Session: worker, Every: contract.DurationMS(time.Hour.Milliseconds()), Wake: &contract.Principal{Kind: contract.PrincipalSession, Session: worker}})
	event, err := s.event(room, engineSender, contract.SessionClosed{Session: worker})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, worker, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SubscriptionByID(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("subscription outlived its session: %v", err)
	}
	if _, problem := s.SubscriptionAdd(ctx, ownerCaller, contract.SubscriptionAddRequest{Scope: contract.ScopeRoom, Room: room, Every: contract.DurationMS(time.Hour.Milliseconds()), Wake: &contract.Principal{Kind: contract.PrincipalSession, Session: worker}}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("waking a closed session: %v", problem)
	}
}

func TestRoomDeletionWaitsForARun(t *testing.T) {
	s, room, _, ctx := setup(t)
	runs, c := withRuns(t, s)
	id := addSubscription(t, s, ctx, runRequest(room))
	c.add(30 * time.Minute)
	pass(t, s, ctx)
	event, err := s.event(room, engineSender, contract.RoomDeleting{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkRoomDeleting(ctx, room, event); err != nil {
		t.Fatal(err)
	}
	ready, err := s.store.RoomsReadyToDelete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 {
		t.Fatalf("room deleted around a Run: %v", ready)
	}
	pass(t, s, ctx)
	if len(runs.removed) != 1 {
		t.Fatalf("a deleting room's Run stayed: %v", runs.removed)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET closed_at=1,state='closed' WHERE room=?", room)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.DeleteRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.SubscriptionByID(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("subscription outlived its room: %v", err)
	}
}

func TestHookPost(t *testing.T) {
	s, room, worker, ctx := setup(t)
	var rootID contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	sub := contract.SubscriptionID("sub_watch")
	beforeRoot, beforeWorker := pendingWakes(t, s, ctx, rootID), pendingWakes(t, s, ctx, worker)
	if _, problem := s.HookPost(ctx, sub, room, contract.PostRequest{Text: "for root"}); problem != nil {
		t.Fatal(problem)
	}
	if got := pendingWakes(t, s, ctx, rootID); got != beforeRoot+1 {
		t.Fatalf("no mention must wake root: %d then %d", beforeRoot, got)
	}
	if _, problem := s.HookPost(ctx, sub, room, contract.PostRequest{Text: "for you", Mentions: []string{"@worker"}}); problem != nil {
		t.Fatal(problem)
	}
	if got := pendingWakes(t, s, ctx, worker); got != beforeWorker+1 {
		t.Fatalf("a mention must wake its session: %d then %d", beforeWorker, got)
	}
	posts := eventsOf[contract.PostCreated](t, s, ctx, room)
	last := posts[len(posts)-1]
	if last.Hook != sub {
		t.Fatalf("post does not name its subscription: %+v", last)
	}
	events, err := s.store.Events(ctx, room, 0, 1000, []contract.EventType{"post.created"})
	if err != nil {
		t.Fatal(err)
	}
	event := events[len(events)-1]
	frame, err := s.inputFrame(ctx, worker, event, contract.Delivery{Surface: contract.SurfaceService, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frame, "engine") || !strings.Contains(frame, "script of subscription sub_watch") {
		t.Fatalf("frame vouches for a script: %q", frame)
	}
	for name, req := range map[string]contract.PostRequest{
		"another room": {Room: "room_other", Text: "x"},
		"no text":      {Text: "  "},
		"attachment":   {Text: "x", Attachments: []contract.Attachment{{Name: "a"}}},
		"dm":           {Text: "x", Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}},
		"unknown name": {Text: "x", Mentions: []string{"@nobody"}},
	} {
		if _, problem := s.HookPost(ctx, sub, room, req); problem == nil {
			t.Errorf("%s: posted", name)
		}
	}
	if _, problem := s.HookPost(ctx, sub, "", contract.PostRequest{Text: "x"}); problem == nil {
		t.Error("a machine Run posted")
	}
}

func TestMigrationGivesExistingRootsTheDefault(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(st, fakeBoxes{}, fakeProfiles{}, func() time.Time { return time.Unix(1000, 0) })
	resetLifecycle(s)
	if err := st.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	made, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	recordNotice(t, s, ctx, made.Room)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Back to the schema before this step, as an installed store is.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DROP TABLE subscription_runs", "DROP TABLE subscription_files", "DROP TABLE subscription_args", "DROP TABLE subscription_events", "DROP TABLE subscriptions",
		"ALTER TABLE repos DROP COLUMN main_head", "DELETE FROM meta WHERE key='subscription_seq'", "UPDATE meta SET value='63' WHERE key='schema_version'",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = New(st, fakeBoxes{}, fakeProfiles{}, s.now)
	resetLifecycle(s)
	info := listed(t, s, ctx, made.Room, store.DefaultSubscriptionID(made.Room))
	if !info.Default || len(info.Subscription.On) == 0 {
		t.Fatalf("migrated default: %+v", info)
	}
	// The notice from before the step fires nothing; a new one does.
	var root contract.SessionID
	if err := st.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=?", made.Room).Scan(&root); err != nil {
		t.Fatal(err)
	}
	before := pendingWakes(t, s, ctx, root)
	pass(t, s, ctx)
	if got := pendingWakes(t, s, ctx, root); got != before {
		t.Fatalf("history replayed: %d then %d", before, got)
	}
	recordNotice(t, s, ctx, made.Room)
	pass(t, s, ctx)
	if got := pendingWakes(t, s, ctx, root); got != before+1 {
		t.Fatalf("after the step: %d then %d", before, got)
	}
}
