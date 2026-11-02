package engine

import (
	"context"
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

func TestSubscriptionAuthority(t *testing.T) {
	s, room, worker, ctx := setup(t)
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
