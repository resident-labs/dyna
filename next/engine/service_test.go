package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

func resetLifecycle(s *Service) {
	s.reconciler = lifecycle.New(s.store, s.boxes, s, s.repos, s.event, func() time.Time { return s.now() })
	s.reconciler.SetRoomData(s)
}

func passLifecycle(t *testing.T, s *Service, ctx context.Context) {
	t.Helper()
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.reconciler.Wait()
}

type BoxSpec = lifecycle.BoxSpec

type fakeBoxes struct{}

func (fakeBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return []string{"sh"}, nil
}

func (fakeBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (fakeBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (fakeBoxes) Remove(context.Context, contract.BoxID) error    { return nil }
func (fakeBoxes) Start(context.Context, contract.BoxID) error     { return nil }
func (fakeBoxes) Stop(context.Context, contract.BoxID) error      { return nil }

func testChannel(t *testing.T, s *Service, ctx context.Context, caller contract.Principal) (<-chan contract.BoxMessage, *contract.Error) {
	t.Helper()
	passLifecycle(t, s, ctx)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxStarting && current.State != contract.BoxReady && current.State != contract.BoxClosing {
		t.Fatalf("test agent box is not ready to connect: %+v", current)
	}
	if current.State == contract.BoxStarting {
		if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
			t.Fatal(problem)
		}
	}
	if allowed, err := s.store.CanDispatch(ctx, caller.Session); err != nil || (!allowed && current.State != contract.BoxClosing) {
		t.Fatalf("test agent cannot dispatch: session=%+v box=%+v err=%v", session, current, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	channel, problem := s.BoxChannel(ctx, caller)
	if problem != nil {
		cancel()
		return nil, problem
	}
	// The channel goroutine reads the store. End it before the store closes and the temp dir is removed.
	t.Cleanup(func() {
		cancel()
		for range channel {
		}
	})
	return channel, nil
}

type fakeProfiles struct{}

func (fakeProfiles) List() []contract.ProfileSummary { return nil }

func (fakeProfiles) CompactionTokens(contract.ProfileName) int { return 0 }

func (fakeProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "model", Thinking: contract.ThinkingLow}, []string{"model"}, nil
}

func setup(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	now := func() time.Time { return time.Unix(1000, 0) }
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := st.AddHost(context.Background(), box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now()}); err != nil {
		t.Fatal(err)
	}
	s := New(st, fakeBoxes{}, fakeProfiles{}, now)
	resetLifecycle(s)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	session, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "worker", Profile: "test", Goal: "goal", Brief: "brief"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	return s, room.Room, session.Session, ctx
}
func setupRoot(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	s, room, worker, ctx := setup(t)
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	return s, room, root, ctx
}
func TestRoomCreatesRootAndAttachesWorker(t *testing.T) {
	s, room, worker, ctx := setup(t)
	var parent contract.SessionID
	var name string
	err := s.store.Read().QueryRowContext(ctx, "SELECT p.id,p.name FROM sessions w JOIN sessions p ON p.id=w.parent WHERE w.id=? AND p.room=? AND p.parent IS NULL", worker, room).Scan(&parent, &name)
	if err != nil || parent == "" || name != "root" {
		t.Fatalf("root %s %q: %v", parent, name, err)
	}
}
func TestSessionBoxSizeDefaults(t *testing.T) {
	s, room, lead, ctx := setup(t)
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", lead).Scan(&root); err != nil {
		t.Fatal(err)
	}
	child, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: lead})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, tc := range []struct {
		id   contract.SessionID
		want contract.BoxSizeName
	}{{root, contract.BoxSizeAgent}, {lead, contract.BoxSizeMedium}, {child.Session, contract.BoxSizeAgent}} {
		session, err := s.store.Session(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.store.Box(ctx, session.Box)
		if err != nil || b.Size.Name != tc.want {
			t.Fatalf("%s: size %s, want %s: %v", tc.id, b.Size.Name, tc.want, err)
		}
	}
}
func TestRootCreationRollsBackAndRootCannotClose(t *testing.T) {
	s, _, worker, ctx := setup(t)
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: root}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("root close: %v", e)
	}
	current, err := s.store.Session(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	event, err := s.event("new-room", owner, contract.SessionStarted{Session: "new-root", Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.store.StartSession(ctx, store.Session{ID: "new-root", Room: "new-room", Name: "root", Profile: "test", Box: current.Box, NewRoomName: "new-room"}, "", contract.BoxSizeAgent, "", event, contract.Event{}, "", false, "", 0)
	if err == nil {
		t.Fatal("expected duplicate box failure")
	}
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM rooms WHERE id='new-room'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("room persisted: %d %v", count, err)
	}
}
func TestSessionCannotDiscardChild(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: parent, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.SessionCloseRequest{Session: child.Session, Discard: true}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("discard: %v", e)
	}
}
func TestParentCloseWaitsForChildSessionAfterBoxDeletion(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: parent, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	connectAgent(ctx, t, s, child.Session)
	connectAgent(ctx, t, s, parent)
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: child.Session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	childAck := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	if _, e := s.ArchiveDone(ctx, childAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: parent}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	parentAck := contract.Principal{Kind: contract.PrincipalSession, Session: parent}
	if _, e := s.ArchiveDone(ctx, parentAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, err := s.store.Session(ctx, child.Session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("child remains open: %v", err)
	}
	if _, err := s.store.Session(ctx, parent); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("parent remains open: %v", err)
	}
}
func TestRepoCISetValidates(t *testing.T) {
	s, room, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.RoomRepoAdd(ctx, owner, contract.RoomRepoAddRequest{Room: room, Repo: "a/b"}); e != nil {
		t.Fatal(e)
	}
	if err := s.store.AddHost(ctx, box.Host{Name: "other", SSH: "user@other", State: box.HostReady}); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil || len(hosts) < 2 {
		t.Fatalf("%+v %v", hosts, err)
	}
	host := func(name contract.HostName, runners int, size contract.BoxSizeName) contract.RepoCIHost {
		return contract.RepoCIHost{Repo: "a/b", Host: name, Runners: runners, Size: size}
	}
	for name, req := range map[string]contract.RepoCISetRequest{
		"bad mode":       {Repo: "a/b", Mode: "cloud"},
		"box, no hosts":  {Repo: "a/b", Mode: contract.CIBox},
		"github, hosts":  {Repo: "a/b", Mode: contract.CIGitHub, Hosts: []contract.RepoCIHost{host("other", 1, contract.BoxSizeLarge)}},
		"unknown host":   {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("nope", 1, contract.BoxSizeLarge)}},
		"no runners":     {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("other", 0, contract.BoxSizeLarge)}},
		"too many":       {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("other", box.RunnerSlots+1, contract.BoxSizeLarge)}},
		"bad size":       {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("other", 1, "huge")}},
		"host twice":     {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("other", 1, contract.BoxSizeLarge), host("other", 2, contract.BoxSizeLarge)}},
		"two sizes":      {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("other", 1, contract.BoxSizeLarge), host(hosts[0].Name, 1, contract.BoxSizeSmall)}},
		"unknown repo":   {Repo: "x/y", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{{Repo: "x/y", Host: "other", Runners: 1, Size: contract.BoxSizeLarge}}},
		"row of another": {Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{{Repo: "x/y", Host: "other", Runners: 1, Size: contract.BoxSizeLarge}}},
	} {
		if _, e := s.RepoCISet(ctx, owner, req); e == nil || e.Code != contract.ErrInvalid {
			t.Errorf("%s: %v", name, e)
		}
	}
	if _, e := s.RepoCISet(ctx, owner, contract.RepoCISetRequest{Repo: "a/b", Mode: contract.CIBox, Hosts: []contract.RepoCIHost{host("other", 2, contract.BoxSizeLarge)}}); e != nil {
		t.Fatal(e)
	}
	if rows, err := s.store.CIHosts(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	if _, e := s.RepoCISet(ctx, owner, contract.RepoCISetRequest{Repo: "a/b", Mode: contract.CIGitHub}); e != nil {
		t.Fatal(e)
	}
	if rows, err := s.store.CIHosts(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestRoomRepoAPI(t *testing.T) {
	s, room, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.RoomRepos(ctx, owner, contract.RoomReposRequest{Room: "missing"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("unknown room: %v", e)
	}
	for _, name := range []contract.RepoName{"bad", "../x", "owner/..", "owner/.", "owner/x/y"} {
		if _, e := s.RoomRepoAdd(ctx, owner, contract.RoomRepoAddRequest{Room: room, Repo: name}); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("invalid %s: %v", name, e)
		}
	}
	if _, e := s.RoomRepoAdd(ctx, owner, contract.RoomRepoAddRequest{Room: room, Repo: "a/b"}); e != nil {
		t.Fatal(e)
	}
	result, e := s.RoomRepos(ctx, owner, contract.RoomReposRequest{Room: room})
	if e != nil || len(result.Repos) != 1 || result.Repos[0].Repo != "a/b" || !result.Repos[0].Default {
		t.Fatalf("repos %+v: %v", result, e)
	}
}

func TestSessionRepositorySelection(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for _, repo := range []contract.RepoName{"owner/a", "owner/b"} {
		if _, e := s.RoomRepoAdd(ctx, owner, contract.RoomRepoAddRequest{Room: room, Repo: repo}); e != nil {
			t.Fatal(e)
		}
	}
	for _, tt := range []struct {
		name            string
		requested, want contract.RepoName
		invalid         bool
	}{
		{"default", "", "owner/a", false}, {"explicit", "owner/b", "owner/b", false}, {"unknown", "owner/c", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: tt.name, Profile: "test", Parent: parent, Repo: tt.requested, Goal: "goal"})
			if tt.invalid {
				if e == nil || e.Code != contract.ErrInvalid {
					t.Fatalf("got %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			got, err := s.store.Session(ctx, child.Session)
			if err != nil || got.Repo != tt.want {
				t.Fatalf("repo %s: %v", got.Repo, err)
			}
			grandchild, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: tt.name + "-child", Profile: "test", Parent: child.Session, Goal: "goal"})
			if e != nil {
				t.Fatal(e)
			}
			inherited, err := s.store.Session(ctx, grandchild.Session)
			if err != nil || inherited.Repo != tt.want {
				t.Fatalf("inherited %s: %v", inherited.Repo, err)
			}
		})
	}
}
func TestSessionStartCreatesWake(t *testing.T) {
	s, room, session, ctx := setup(t)
	events, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	var body contract.SessionStarted
	for _, event := range events.Events {
		if started, ok := event.Body.(contract.SessionStarted); ok && started.Session == session {
			body = started
		}
	}
	if body.Brief != "brief" || body.Goal != "goal" {
		t.Fatalf("unexpected start: %+v", body)
	}
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	start, ok := (<-channel).Body.(contract.TurnStart)
	if !ok || len(start.Inputs) != 1 || inputBody(start.Inputs[0].Text) != "Goal: goal\n\nbrief" {
		t.Fatalf("unexpected wake: %+v", start)
	}
}
func TestSessionStartWithoutGoalOrBriefHasNoWake(t *testing.T) {
	s, room, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	empty, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "empty", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	briefOnly, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "brief", Profile: "test", Brief: "brief only"})
	if e != nil {
		t.Fatal(e)
	}
	var deliveries int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=?", empty.Session).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("empty start deliveries %d: %v", deliveries, err)
	}
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: briefOnly.Session})
	if e != nil {
		t.Fatal(e)
	}
	start, ok := (<-channel).Body.(contract.TurnStart)
	if !ok || len(start.Inputs) != 1 || inputBody(start.Inputs[0].Text) != "brief only" {
		t.Fatalf("unexpected brief wake: %+v", start)
	}
}
func TestRejectUnhandledDelivery(t *testing.T) {
	s, room, session, ctx := setup(t)
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.RoomPaused{})
	if err != nil {
		t.Fatal(err)
	}
	delivery := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: session}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}
	if _, err := s.store.Post(ctx, event, []contract.Delivery{delivery}); err == nil {
		t.Fatal("accepted unrenderable delivery")
	}
	var count int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE id=?", event.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("event persisted: count=%d err=%v", count, err)
	}
}

func TestMentionSteersRunningTurn(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-channel).Body.(contract.TurnStart); !ok {
		t.Fatal("expected start")
	}
	_, e = s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "hello", Mentions: []string{"worker"}})
	if e != nil {
		t.Fatal(e)
	}
	steer, ok := (<-channel).Body.(contract.TurnSteer)
	if !ok || steer.Queue != contract.QueueSteer || inputBody(steer.Input.Text) != "hello" {
		t.Fatalf("unexpected steer: %+v", steer)
	}
}
func TestReconnectResendsTurnStartAndUnreadSteers(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	firstCtx, cancel := context.WithCancel(ctx)
	channel, e := testChannel(t, s, firstCtx, caller)
	if e != nil {
		t.Fatal(e)
	}
	start := (<-channel).Body.(contract.TurnStart)
	_, e = s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: start.Inputs[0].Delivery})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "again", Mentions: []string{"worker"}})
	if e != nil {
		t.Fatal(e)
	}
	steer := (<-channel).Body.(contract.TurnSteer)
	cancel()
	reconnect, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	restarted, ok := (<-reconnect).Body.(contract.TurnStart)
	if !ok || len(restarted.Inputs) != 1 || restarted.Inputs[0].Delivery != start.Inputs[0].Delivery {
		t.Fatalf("unexpected restart: %+v", restarted)
	}
	replayed, ok := (<-reconnect).Body.(contract.TurnSteer)
	if !ok || replayed.Input.Delivery != steer.Input.Delivery {
		t.Fatalf("unexpected replay: %+v", replayed)
	}
}
func TestFailedResultReachesParent(t *testing.T) {
	s, room, parent, ctx := setup(t)
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent})
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	child, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.TurnEnded{Session: child.Session, Outcome: contract.TurnFailed, Error: "tool failed"})
	if err != nil {
		t.Fatal(err)
	}
	d := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: parent}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyPost}, Wake: true}
	if _, err := s.store.Post(ctx, event, []contract.Delivery{d}); err != nil {
		t.Fatal(err)
	}
	steer := (<-channel).Body.(contract.TurnSteer)
	if inputBody(steer.Input.Text) != "turn failed: tool failed" {
		t.Fatalf("text %q", steer.Input.Text)
	}
}

func TestFailedTurnReturnsInputsWithoutWaking(t *testing.T) {
	for _, tc := range []struct {
		name           string
		outcome        contract.TurnOutcome
		cause          contract.FailureCause
		read, returned bool
	}{
		{"failed taken", contract.TurnFailed, contract.CauseModelFailed, false, true},
		{"failed read", contract.TurnFailed, contract.CauseModelFailed, true, true},
		{"output limit", contract.TurnFailed, contract.CauseOutputLimit, true, false},
		{"stopped", contract.TurnStopped, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, room, session, ctx := setup(t)
			caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
			channelCtx, cancel := context.WithCancel(ctx)
			channel, problem := testChannel(t, s, channelCtx, caller)
			if problem != nil {
				t.Fatal(problem)
			}
			start := (<-channel).Body.(contract.TurnStart)
			if len(start.Inputs) != 1 {
				t.Fatalf("first turn inputs: %+v", start.Inputs)
			}
			first := start.Inputs[0]
			if tc.read {
				if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: first.Delivery}); problem != nil {
					t.Fatal(problem)
				}
			}
			cancel()
			for range channel {
			}
			output := contract.TurnOutputRequest{Turn: start.Turn, Outcome: tc.outcome, Cause: tc.cause}
			if _, problem := s.TurnOutput(ctx, caller, output); problem != nil {
				t.Fatal(problem)
			}
			event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				t.Fatal(err)
			}
			turn, inputs, _, err := s.store.Dispatch(ctx, session, "retry", event, false)
			if err != nil || turn != "" || len(inputs) != 0 {
				t.Fatalf("started without wake: turn=%q inputs=%+v err=%v", turn, inputs, err)
			}
			var pending int
			if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND state='pending' AND wake=0 AND turn IS NULL AND queue IS NULL", session).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.returned {
				want = 1
			}
			if pending != want {
				t.Fatalf("returned pending inputs=%d want %d", pending, want)
			}
			if !tc.returned {
				return
			}
			post, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "next", Mentions: []string{"worker"}})
			if problem != nil {
				t.Fatal(problem)
			}
			event, err = s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				t.Fatal(err)
			}
			turn, inputs, _, err = s.store.Dispatch(ctx, session, "retry", event, false)
			if err != nil || turn != "retry" || len(inputs) != 2 || inputs[0].Seq != first.Delivery || int64(inputs[1].Seq) != post.Seq {
				t.Fatalf("retry: turn=%q inputs=%+v err=%v", turn, inputs, err)
			}
			if _, problem := s.TurnOutput(ctx, caller, output); problem != nil {
				t.Fatalf("replay: %v", problem)
			}
			if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
				t.Fatal(problem)
			}
			list, problem := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"turn.ended"}})
			if problem != nil {
				t.Fatal(problem)
			}
			var matched bool
			for _, event := range list.Events {
				ended := event.Body.(contract.TurnEnded)
				if ended.Turn == turn {
					for _, reply := range ended.Replies {
						if reply.Reply == first.Reply && reply.Posted == "" {
							matched = true
						}
					}
				}
			}
			if !matched {
				t.Fatalf("final did not reach first reply target: %+v", list.Events)
			}
		})
	}
}

func TestFailedTurnRecordsError(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	start := (<-channel).Body.(contract.TurnStart)
	if _, e = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Cause: contract.CauseModelFailed}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("completed turn with cause: %+v", e)
	}
	_, e = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnFailed, Error: "failed", Cause: contract.CauseModelFailed, Model: "model"})
	if e != nil {
		t.Fatal(e)
	}
	list, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Limit: 100, Types: []contract.EventType{"turn.ended"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(list.Events) != 1 {
		t.Fatalf("got %d events", len(list.Events))
	}
	ended := list.Events[0].Body.(contract.TurnEnded)
	if ended.Error != "failed" || ended.Cause != contract.CauseModelFailed || ended.Usage.Model != "model" {
		t.Fatalf("unexpected outcome: %+v", ended)
	}
}
func TestPostAcrossRooms(t *testing.T) {
	s, source, sender, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: other.Room, Name: "target", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	channel, e := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: target.Session})
	if e != nil {
		t.Fatal(e)
	}
	<-channel
	_, e = s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sender}, contract.PostRequest{Room: source, Text: "cross-room", Mentions: []string{"@other:target"}})
	if e != nil {
		t.Fatal(e)
	}
	steer := (<-channel).Body.(contract.TurnSteer)
	if inputBody(steer.Input.Text) != "cross-room" || steer.Input.Reply.Kind != contract.ReplyPost || steer.Input.Reply.Target.Session != sender {
		t.Fatalf("got %+v", steer)
	}
}

func TestUnknownPostAddress(t *testing.T) {
	s, room, _, ctx := setup(t)
	for _, address := range []string{"@absent", "@absent:agent"} {
		_, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "hello", Mentions: []string{address}})
		if e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("%s: %v", address, e)
		}
	}
}

func TestPostToOwnerDM(t *testing.T) {
	s, room, session, ctx := setup(t)
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	home := contract.Conversation{Kind: contract.ConversationDM, Session: session}
	post, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "direct", Home: home})
	if e != nil {
		t.Fatal(e)
	}
	started, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	_, inputs, _, err := s.store.Dispatch(ctx, session, "turn", started, false)
	if err != nil || len(inputs) != 2 || inputs[1].Reply.Kind != contract.ReplyFinal || inputs[1].Reply.Home != home {
		t.Fatalf("post %v inputs %+v: %v", post, inputs, err)
	}
}

func TestOwnPostDoesNotWake(t *testing.T) {
	s, room, session, ctx := setup(t)
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	_, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.PostRequest{Room: room, Text: "self", Mentions: []string{"worker"}})
	if e != nil {
		t.Fatal(e)
	}
	turn := contract.TurnID("turn_test")
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	_, deliveries, _, err := s.store.Dispatch(ctx, session, turn, event, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("got %d deliveries, want only initial wake", len(deliveries))
	}
}

func TestTurnOutputRetryIsIdempotent(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	start := (<-channel).Body.(contract.TurnStart)
	req := contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "done", Model: "model"}
	if _, e := s.TurnOutput(ctx, caller, req); e != nil {
		t.Fatal(e)
	}
	if _, e := s.TurnOutput(ctx, caller, req); e != nil {
		t.Fatal(e)
	}
	req.Final = "different"
	if _, e := s.TurnOutput(ctx, caller, req); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("expected conflict, got %v", e)
	}
	list, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Limit: 100, Types: []contract.EventType{"turn.ended"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(list.Events) != 1 {
		t.Fatalf("got %d turn-ended events", len(list.Events))
	}
}

type unavailableBoxes struct{}

func (unavailableBoxes) Create(context.Context, BoxSpec) (string, error) {
	return "", errors.New("host unavailable")
}
func (unavailableBoxes) Remove(context.Context, contract.BoxID) error { return nil }

func TestStartErrorReturnsReachableFailedSession(t *testing.T) {
	s, room, _, ctx := setup(t)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.boxes = unavailableBoxes{}
	resetLifecycle(s)
	response, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "pending", Profile: "test"})
	if e != nil || response.Session == "" || response.Box == "" {
		t.Fatalf("got %+v, %v", response, e)
	}
	passLifecycle(t, s, ctx)
	now = now.Add(30 * time.Second)
	passLifecycle(t, s, ctx)
	now = now.Add(time.Minute)
	passLifecycle(t, s, ctx)
	list, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"box.changed"}})
	if e != nil {
		t.Fatal(e)
	}
	var last contract.BoxChanged
	var found bool
	for _, event := range list.Events {
		change := event.Body.(contract.BoxChanged)
		if change.Box == response.Box {
			last, found = change, true
		}
	}
	if !found || last.Session != response.Session || last.To != contract.BoxFailed || last.Reason != string(contract.CauseCreateFailed) || last.Cause != contract.CauseCreateFailed {
		t.Fatalf("unexpected placement: %+v", last)
	}
}

func TestSessionCannotClose(t *testing.T) {
	s, _, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.SessionClose(ctx, caller, contract.SessionCloseRequest{Session: session}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("close: %v", e)
	}
}

func TestBoxLifecycleEmitsTransitions(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	session := lead.Session
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	connectAgent(ctx, t, s, session)
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	list, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"box.changed"}})
	if e != nil {
		t.Fatal(e)
	}
	want := []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxReady, contract.BoxClosing, contract.BoxDeleted}
	var got []contract.BoxState
	for _, event := range list.Events {
		change := event.Body.(contract.BoxChanged)
		if change.Session == session {
			got = append(got, change.To)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transitions %v, want %v", got, want)
	}
}

func TestSessionStartRejectsUnknownParent(t *testing.T) {
	s, room, _, ctx := setup(t)
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: "missing"})
	if e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("expected invalid, got %v", e)
	}
}

type blockingBoxes struct {
	started   chan BoxSpec
	cancelled chan struct{}
	removed   chan contract.BoxID
}

func (b blockingBoxes) Create(ctx context.Context, spec BoxSpec) (string, error) {
	b.started <- spec
	<-ctx.Done()
	close(b.cancelled)
	return "", ctx.Err()
}
func (b blockingBoxes) Remove(_ context.Context, id contract.BoxID) error {
	b.removed <- id
	return nil
}

func TestCloseCancelsCreatingBox(t *testing.T) {
	s, room, root, ctx := setup(t)
	adapter := blockingBoxes{started: make(chan BoxSpec, 1), cancelled: make(chan struct{}), removed: make(chan contract.BoxID, 1)}
	s.boxes = adapter
	resetLifecycle(s)
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "blocked", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	spec := <-adapter.started
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET parent=? WHERE id=?", root, spec.Session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: spec.Session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	select {
	case <-adapter.cancelled:
	default:
		t.Fatal("close did not cancel create")
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	select {
	case id := <-adapter.removed:
		if id != spec.Box {
			t.Fatalf("removed box %s, want %s", id, spec.Box)
		}
	default:
		t.Fatal("close did not remove the box")
	}
	b, err := s.store.Box(ctx, spec.Box)
	if err != nil || b.State != contract.BoxDeleted {
		t.Fatalf("box: %+v, %v", b, err)
	}
}

type cancelOnRemove struct{ cancel context.CancelFunc }

func (cancelOnRemove) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (b cancelOnRemove) Remove(context.Context, contract.BoxID) error  { b.cancel(); return nil }

func TestSessionStartWithoutFittingHostEmitsPending(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	s := New(st, fakeBoxes{}, fakeProfiles{}, now)
	resetLifecycle(s)
	ctx := context.Background()
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	worker, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "worker", Profile: "test", Goal: "goal", Brief: "brief"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	events, e := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room.Room, Types: []contract.EventType{"box.changed"}})
	if e != nil {
		t.Fatal(e)
	}
	var changed contract.BoxChanged
	for _, event := range events.Events {
		if body := event.Body.(contract.BoxChanged); body.Session == worker.Session {
			changed = body
		}
	}
	if changed.To != contract.BoxPending || changed.Reason != string(box.PendingNoReadyHost) {
		t.Fatalf("unexpected box change: %+v", changed)
	}
}
func TestClosePendingRepositoryFreeChildWithoutArchiveAck(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	s := New(st, fakeBoxes{}, fakeProfiles{}, now)
	resetLifecycle(s)
	ctx := context.Background()
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	worker, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "worker", Profile: "test", Goal: "goal", Brief: "brief"})
	if e != nil {
		t.Fatal(e)
	}
	record, err := st.Session(ctx, worker.Session)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := st.Box(ctx, record.Box); err != nil || current.State != contract.BoxPending {
		t.Fatalf("box %+v, error %v, want pending before close", current, err)
	}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: worker.Session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	current, err := st.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v, want deleted without an archive ack", current, err)
	}
	if _, err := st.Session(ctx, worker.Session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session remains open: %v", err)
	}
}

func TestResumeClosesPendingBoxWithoutArchiveAck(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	s := New(st, fakeBoxes{}, fakeProfiles{}, now)
	resetLifecycle(s)
	ctx := context.Background()
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	worker, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "worker", Profile: "test", Goal: "goal", Brief: "brief"})
	if e != nil {
		t.Fatal(e)
	}
	record, err := st.Session(ctx, worker.Session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	// A restart after the close intent is saved resumes the pending box close.
	if err := st.MarkClosing(ctx, worker.Session, false, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	restarted := New(st, fakeBoxes{}, fakeProfiles{}, now)
	passLifecycle(t, restarted, ctx)
	passLifecycle(t, restarted, ctx)
	current, err = st.Box(ctx, record.Box)
	if err != nil || current.State != contract.BoxDeleted {
		t.Fatalf("box %+v, error %v, want deleted without an archive ack", current, err)
	}
	if _, err := st.Session(ctx, worker.Session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session remains open: %v", err)
	}
}

func TestCloseRetriesAfterBoxDeletion(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	session := lead.Session
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	connectAgent(ctx, t, s, session)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	events, e := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room, Types: []contract.EventType{"box.changed"}})
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, event := range events.Events {
		if event.Body.(contract.BoxChanged).To == contract.BoxDeleted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d deleted events", count)
	}
}

type recordingBoxes struct{ specs []BoxSpec }

func (b *recordingBoxes) Create(_ context.Context, spec BoxSpec) (string, error) {
	b.specs = append(b.specs, spec)
	return "image", nil
}
func (b *recordingBoxes) Remove(context.Context, contract.BoxID) error { return nil }

func TestChildPlacement(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	recorder := &recordingBoxes{}
	s.boxes = recorder
	resetLifecycle(s)
	start := func(parent contract.SessionID, name string) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent, Goal: "goal"})
		if e != nil {
			t.Fatal(e)
		}
		passLifecycle(t, s, ctx)
		return r
	}
	lead := start(root, "lead")
	if err := s.store.AddHost(ctx, box.Host{Name: "larger", State: box.HostReady, CPUs: 64, MemBytes: 128 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	child := start(lead.Session, "child")
	grandchild := start(child.Session, "grandchild")
	for i, id := range []contract.BoxID{lead.Box, child.Box, grandchild.Box} {
		want, err := box.SliceName(id)
		if err != nil {
			t.Fatal(err)
		}
		if recorder.specs[i].Slice != want || recorder.specs[i].Host != []contract.HostName{"host", "larger", "larger"}[i] {
			t.Fatalf("placement %d: %+v, want %s on host", i, recorder.specs[i], want)
		}
	}
}

type heldParentBox struct {
	first    atomic.Bool
	started  chan BoxSpec
	release  chan struct{}
	children chan BoxSpec
	finished chan contract.BoxID
	blocked  contract.BoxID
	unblock  chan struct{}
}

func (b *heldParentBox) Create(_ context.Context, spec BoxSpec) (string, error) {
	if b.first.CompareAndSwap(false, true) {
		b.started <- spec
		<-b.release
	} else {
		b.children <- spec
		if spec.Box == b.blocked {
			<-b.unblock
		}
		b.finished <- spec.Box
	}
	return "image", nil
}
func (*heldParentBox) Remove(context.Context, contract.BoxID) error { return nil }

func TestPendingChildrenRetryIndependently(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetRepositories(placementRepos{})
	adapter := &heldParentBox{started: make(chan BoxSpec), release: make(chan struct{}), children: make(chan BoxSpec, 2), finished: make(chan contract.BoxID, 2), unblock: make(chan struct{})}
	s.boxes = adapter
	resetLifecycle(s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	parent, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	spec := <-adapter.started
	start := func(name string) contract.SessionStartResponse {
		t.Helper()
		child, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent.Session, Goal: "goal"})
		if problem != nil {
			t.Fatal(problem)
		}
		return child
	}
	a, b := start("a"), start("b")
	adapter.blocked = a.Box
	close(adapter.release)
	s.reconciler.Wait()
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		child := <-adapter.children
		want, err := box.SliceName(child.Box)
		if err != nil || child.Slice != want || child.Host != spec.Host || len(child.Repos) != 1 || child.Repos[0].Name != "owner/repo" {
			t.Fatalf("placement: %+v", child)
		}
	}
	if got := <-adapter.finished; got != b.Box {
		t.Fatalf("unblocked child = %s, want %s", got, b.Box)
	}
	close(adapter.unblock)
	s.reconciler.Wait()
	if got := <-adapter.finished; got != a.Box {
		t.Fatalf("blocked child = %s, want %s", got, a.Box)
	}
}

func TestCloseRootIsForbidden(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: root}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("close root: %v", e)
	}
}

func TestStartRejectsClosingParent(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closing' WHERE id=?", root)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: root, Goal: "goal"})
	if e == nil || e.Code != contract.ErrParentClosing {
		t.Fatalf("start: %v", e)
	}
}

type closingInterleaveBox struct {
	started   chan BoxSpec
	cancelled chan struct{}
	release   chan struct{}
}

func (b closingInterleaveBox) Create(ctx context.Context, spec BoxSpec) (string, error) {
	b.started <- spec
	<-ctx.Done()
	close(b.cancelled)
	<-b.release
	return "", ctx.Err()
}
func (closingInterleaveBox) Remove(context.Context, contract.BoxID) error { return nil }

func TestClosingCreatingParentRejectsConcurrentChild(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	adapter := closingInterleaveBox{started: make(chan BoxSpec), cancelled: make(chan struct{}), release: make(chan struct{})}
	s.boxes = adapter
	resetLifecycle(s)
	_, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	parent := <-adapter.started
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: parent.Session}); e != nil {
		t.Fatal(e)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	<-adapter.cancelled
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: parent.Session, Goal: "goal"})
	if e == nil {
		t.Error("child started while parent was closing")
	}
	close(adapter.release)
	s.reconciler.Wait()
}

type heldRemoveBox struct {
	started chan struct{}
	release chan struct{}
	removed chan struct{}
}

func (b heldRemoveBox) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (b heldRemoveBox) Remove(context.Context, contract.BoxID) error {
	b.started <- struct{}{}
	<-b.release
	b.removed <- struct{}{}
	return nil
}

func TestConcurrentCloseRemovesBoxOnce(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	connectAgent(ctx, t, s, lead.Session)
	adapter := heldRemoveBox{started: make(chan struct{}, 2), release: make(chan struct{}), removed: make(chan struct{}, 2)}
	s.boxes = adapter
	resetLifecycle(s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: lead.Session}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	ack := contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}
	if _, e := s.ArchiveDone(ctx, ack, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	<-adapter.started
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	close(adapter.release)
	s.reconciler.Wait()
	if n := len(adapter.removed); n != 1 {
		t.Fatalf("removed %d times", n)
	}
}

func TestShellJobEndWakesOnceAndSteersARunningTurn(t *testing.T) {
	s, _, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := testChannel(t, s, ctx, caller)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := (<-channel).Body.(contract.TurnStart); !ok {
		t.Fatal("expected start")
	}
	ended := contract.ShellJobEndedRequest{Job: "job_1", Description: "Build the bundle", End: &contract.ShellJobEnd{Kind: contract.ShellJobSignaled, Signal: "SIGTERM"}, ExitCode: -1, Tail: "boom"}
	for range 2 { // the box agent retries after a lost reply
		if _, e = s.ShellJobEnded(ctx, caller, ended); e != nil {
			t.Fatal(e)
		}
	}
	steer, ok := (<-channel).Body.(contract.TurnSteer)
	if !ok || steer.Queue != contract.QueueSteer || inputBody(steer.Input.Text) != "Background job 'Build the bundle' ended by a signal (SIGTERM). Last output:\nboom" {
		t.Fatalf("unexpected steer: %+v", steer)
	}
	var wakes int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='shell.job.ended'").Scan(&wakes); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM shell_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if wakes != 1 || jobs != 1 {
		t.Fatalf("wakes = %d, jobs = %d, want 1 each", wakes, jobs)
	}
}

func TestPostRejectsMissingAttachment(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	_, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{
		Room: room, Text: "hello", Attachments: []contract.Attachment{{Name: "missing", Media: "text/plain", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}},
	})
	if e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("got %v", e)
	}
}

func TestPostNeedsTextOrFile(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	hash := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := os.MkdirAll(s.layout.Files(room), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.layout.File(room, hash), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: " "}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("empty post: got %v", e)
	}
	file := contract.Attachment{Name: "notes.txt", Media: "text/plain", SHA256: hash}
	if _, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Attachments: []contract.Attachment{file}}); e != nil {
		t.Fatalf("file-only post: %v", e)
	}
}

// connectAgent marks the session's box as connected, as a box hello does, and records a turn, so its close waits for an archive ack.
func connectAgent(ctx context.Context, t *testing.T, s *Service, session contract.SessionID) {
	t.Helper()
	passLifecycle(t, s, ctx)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, record.Room, current, contract.BoxReady, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) SELECT 'turn_'||?,?,0,'','',0,0,0,0,0 WHERE NOT EXISTS (SELECT 1 FROM turns WHERE session=?)", session, session, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// ownerStream opens an owner event stream, of a room or of every room, that ends before the store
// closes. Its cleanup runs before setup's: it cancels the stream and waits for the stream's goroutine,
// which may still be reading the store, so no read reopens a database file during TempDir cleanup.
func ownerStream(ctx context.Context, t *testing.T, s *Service, room contract.RoomID, after int64) <-chan contract.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	stream, e := s.Events(ctx, contract.Principal{Kind: contract.PrincipalOwner}, room, after)
	if e != nil {
		cancel()
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cancel()
		for range stream {
		}
	})
	return stream
}

func TestSessionListLastOutcomeSkipsStoppedTurns(t *testing.T) {
	s, room, session, ctx := setup(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES ('a',?,0,1,'failed','',0,0,0,0),('b',?,2,3,'stopped','',0,0,0,0)", session, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	list, e := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, item := range list.Sessions {
		if item.ID == session {
			found = true
			if item.LastOutcome != contract.TurnFailed {
				t.Fatalf("a stopped turn hid the failure: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("session %s is not in the list: %+v", session, list.Sessions)
	}
}

func TestClosedSessionNamesRemainInEvents(t *testing.T) {
	s, room, session, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.PostRequest{Room: room, Text: "hello"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session, Discard: true}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	list, e := s.SessionList(ctx, owner, contract.SessionListRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range list.Sessions {
		if item.ID == session {
			t.Fatal("closed session listed")
		}
	}
	events, e := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room, Types: []contract.EventType{"post.created"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(events.Events) != 1 || events.Names[session] != "worker" {
		t.Fatalf("events: %+v, names: %+v", events.Events, events.Names)
	}
}

func TestRoomAndSessionNamesAreMentionSafe(t *testing.T) {
	s, room, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"Agent_1.test-2", true}, {"has space", false}, {"line\nbreak", false}, {"_leading", false}, {strings.Repeat("a", 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, roomErr := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: tc.name, RootProfile: "test"})
			_, sessionErr := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: tc.name, Profile: "test"})
			if tc.valid {
				if roomErr != nil || sessionErr != nil {
					t.Fatalf("valid name: room %v, session %v", roomErr, sessionErr)
				}
			} else if roomErr == nil || roomErr.Code != contract.ErrInvalid || sessionErr == nil || sessionErr.Code != contract.ErrInvalid {
				t.Fatalf("invalid name: room %v, session %v", roomErr, sessionErr)
			}
		})
	}
	for _, name := range []string{"root", "parent", "children", "siblings"} {
		if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "test"}); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("role name %s: %v", name, e)
		}
	}
}

func TestSessionListTurnState(t *testing.T) {
	s, room, session, ctx := setup(t)
	connectAgent(ctx, t, s, session)
	posted, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "hello"})
	if e != nil {
		t.Fatal(e)
	}
	list, e := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: room})
	if e != nil || len(list.Sessions) != 2 || list.Seq != posted.Seq {
		t.Fatalf("list %+v, error %v", list, e)
	}
	for _, item := range list.Sessions {
		want := contract.TurnID("")
		if item.ID == session {
			want = contract.TurnID("turn_" + session)
		}
		if item.Turn != want {
			t.Fatalf("session %s turn %q, want %q", item.Name, item.Turn, want)
		}
	}
}

func TestOwnerAttention(t *testing.T) {
	s, a, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	b, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for _, row := range []struct{ id, class string }{{"owner-op", "owner"}, {"agent-op", "agent"}} {
			if _, err := tx.Exec(`INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES(?, ?, ?, 'owner', '', ?, 'pending', '{}', '{}', x'00', 1, 2)`, row.id, row.id, a, row.class); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('running',?,1,NULL,'','',0,0,0,0),('ended',(SELECT id FROM sessions WHERE room=? AND parent IS NULL),1,2,'','',0,0,0,0)`, worker, b.Room); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	list, problem := s.RoomList(ctx, owner, contract.RoomListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	var newest int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT MAX(seq) FROM events").Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if list.Seq < newest || len(list.Rooms) != 2 {
		t.Fatalf("list: %+v", list)
	}
	for _, room := range list.Rooms {
		if room.Waiting == nil || room.Working == nil {
			t.Fatalf("nil lists: %+v", room)
		}
		if room.ID == a && (!reflect.DeepEqual(room.Waiting, []contract.OpID{"owner-op"}) || !reflect.DeepEqual(room.Working, []contract.SessionID{worker})) {
			t.Fatalf("room A: %+v", room)
		}
		if room.ID == b.Room && (len(room.Waiting) != 0 || len(room.Working) != 0) {
			t.Fatalf("room B: %+v", room)
		}
	}
	stream := ownerStream(ctx, t, s, "", list.Seq)
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		for _, event := range []contract.Event{
			{ID: "attention-post", Room: a, Body: contract.PostCreated{Text: "skip"}},
			{ID: "attention-op", Room: a, Body: contract.OpRequested{}},
			{ID: "attention-turn", Room: b.Room, Body: contract.TurnStarted{}},
			{ID: "attention-session", Room: b.Room, Body: contract.SessionStarted{Session: "new-session"}},
		} {
			event.Time = time.Unix(1000, 0)
			event.From = contract.Principal{Kind: contract.PrincipalEngine}
			if _, err := s.store.AppendEvent(tx, event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Home counts a room's sessions from session.started and session.closed.
	for _, id := range []contract.EventID{"attention-op", "attention-turn", "attention-session"} {
		event := <-stream
		if event.ID != id {
			t.Fatalf("got %s, want %s", event.ID, id)
		}
	}
	if _, problem := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: worker, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	closed := <-stream
	for _, ok := closed.Body.(contract.BoxChanged); ok; _, ok = closed.Body.(contract.BoxChanged) {
		closed = <-stream // the owner stream now carries box.changed too
	}
	if body, ok := closed.Body.(contract.SessionClosed); !ok || body.Session != worker {
		t.Fatalf("expected session.closed for %s, got %+v", worker, closed)
	}
	list, problem = s.RoomList(ctx, owner, contract.RoomListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, room := range list.Rooms {
		if room.ID == a && len(room.Working) != 0 {
			t.Fatalf("closed session still working: %+v", room)
		}
	}
}

func TestPostWakesOnlyTargetBox(t *testing.T) {
	s, room, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	a, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "a", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	b, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "b", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	wakes := make(chan []contract.SessionID, 1)
	s.store.SetWake(func(ids []contract.SessionID, events []contract.Event) {
		s.queueWakes(ids, events)
		for _, event := range events {
			if post, ok := event.Body.(contract.PostCreated); ok && post.Text == "hello" {
				wakes <- append([]contract.SessionID(nil), ids...)
			}
		}
	})
	aMessages, problem := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: a.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: b.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = s.Post(ctx, owner, contract.PostRequest{Room: room, Text: "hello", Mentions: []string{"a"}})
	if problem != nil {
		t.Fatal(problem)
	}
	if start, ok := (<-aMessages).Body.(contract.TurnStart); !ok || len(start.Inputs) != 1 || inputBody(start.Inputs[0].Text) != "hello" {
		t.Fatalf("unexpected start: %+v", start)
	}
	if ids := <-wakes; len(ids) != 1 || ids[0] != a.Session {
		t.Fatalf("post woke %v, want only %s", ids, a.Session)
	}
}

func TestSessionStartSize(t *testing.T) {
	s, room, lead, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	sized, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "sized", Profile: "test", Size: contract.BoxSizeLarge})
	if e != nil {
		t.Fatal(e)
	}
	if got, err := s.store.Box(ctx, sized.Box); err != nil || got.Size.Name != contract.BoxSizeLarge {
		t.Fatalf("size %v: %v", got.Size.Name, err)
	}
	if _, e = s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "bad", Profile: "test", Size: "huge"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("unknown size: %v", e)
	}
	if _, e = s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: lead, Size: contract.BoxSizeSmall}); e != nil {
		t.Fatalf("child size: %v", e)
	}
}

func TestSessionSetProfileAuthority(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	sibling, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "sibling", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	req := contract.SessionSetProfileRequest{Session: worker, Profile: "other"}
	if _, e = s.SessionSetProfile(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sibling.Session}, req); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("sibling: %v", e)
	}
	if _, e = s.SessionSetProfile(ctx, owner, req); e != nil {
		t.Fatalf("owner: %v", e)
	}
}

func TestSessionSetProfileEventAndWake(t *testing.T) {
	s, _, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	wakes := func() []bool {
		rows, err := s.store.Read().QueryContext(ctx, "SELECT d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='session.profile_changed' AND d.to_id=? ORDER BY d.seq", worker)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []bool
		for rows.Next() {
			var wake bool
			if err := rows.Scan(&wake); err != nil {
				t.Fatal(err)
			}
			got = append(got, wake)
		}
		return got
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: worker, Profile: "other"}); e != nil {
		t.Fatal(e)
	}
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t',?,1,2,'failed','',0,0,0,0)", worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: worker, Profile: "test"}); e != nil {
		t.Fatal(e)
	}
	if got := wakes(); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("wake flags %v, want [false true]", got)
	}
	if got, err := s.store.Session(ctx, worker); err != nil || got.Profile != "test" {
		t.Fatalf("profile %q: %v", got.Profile, err)
	}
}

func TestSetModelFailureKeepsInputForTheProfileWake(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channelCtx, cancel := context.WithCancel(ctx)
	channel, problem := testChannel(t, s, channelCtx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-channel).Body.(contract.TurnStart)
	cancel()
	for range channel {
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnFailed, Error: "no such model", Cause: contract.CauseSetModelFailed}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SessionSetProfile(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionSetProfileRequest{Session: session, Profile: "other"}); problem != nil {
		t.Fatal(problem)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, inputs, profile, err := s.store.Dispatch(ctx, session, "retry", event, false)
	if err != nil || turn != "retry" || profile != "other" || len(inputs) != 2 || inputs[0].Seq != start.Inputs[0].Delivery {
		t.Fatalf("turn=%q profile=%q inputs=%+v err=%v", turn, profile, inputs, err)
	}
}

func TestSessionStartClientIDRetry(t *testing.T) {
	s, room, _, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalOwner}
	req := contract.SessionStartRequest{Room: room, Name: "retry", Profile: "test", ClientID: "retry-key"}
	type result struct {
		response contract.SessionStartResponse
		problem  *contract.Error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			response, problem := s.SessionStart(ctx, caller, req)
			results <- result{response, problem}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.problem != nil || second.problem != nil || first.response != second.response {
		t.Fatalf("retry: %+v %+v", first, second)
	}
}

// A concurrent move and start cannot leave the new child beyond the depth limit.
func TestConcurrentStartAndMoveRespectDepth(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	sibling, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "sibling", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	s.maxDepth = 3
	start := make(chan struct{})
	results := make(chan *contract.Error, 2)
	go func() {
		<-start
		_, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: worker, Name: "child", Profile: "test"})
		results <- e
	}()
	go func() {
		<-start
		_, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: worker, NewParent: sibling.Session})
		results <- e
	}()
	close(start)
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("want exactly one success, got %v and %v", a, b)
	}
}

// A slow stream must replay after its subscription overflows, without losing order. The first live event also checks stored times.
func TestStreamOverflowReplaysEvents(t *testing.T) {
	s, room, _, ctx := setup(t)
	var after int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM events").Scan(&after); err != nil {
		t.Fatal(err)
	}
	stream, problem := s.Events(ctx, contract.Principal{Kind: contract.PrincipalOwner}, room, after)
	if problem != nil {
		t.Fatal(problem)
	}
	write := func(i int, target contract.RoomID) {
		t.Helper()
		event := contract.Event{ID: contract.EventID(fmt.Sprintf("stream_%d", i)), Room: target, Time: time.Unix(100, 123456789), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: fmt.Sprint(i)}}
		if err := s.store.RecordEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	write(0, room)
	first := <-stream
	if first.Body.(contract.PostCreated).Text != "0" || !first.Time.Equal(time.UnixMilli(100123).UTC()) || first.Time.Location() != time.UTC {
		t.Fatalf("first event: %+v", first)
	}
	// Block output, then fill and overflow the store subscription.
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for i := 1; i <= 150; i++ {
			_, err := s.store.AppendEvent(tx, contract.Event{ID: contract.EventID(fmt.Sprintf("stream_%d", i)), Room: room, Time: time.Unix(100, 123456789), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: fmt.Sprint(i)}})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 150; i++ {
		event := <-stream
		if event.Body.(contract.PostCreated).Text != fmt.Sprint(i) || event.Seq <= first.Seq {
			t.Fatalf("event %d: %+v", i, event)
		}
		first = event
	}
}
