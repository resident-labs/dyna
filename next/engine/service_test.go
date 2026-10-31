package engine

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type fakeBoxes struct{}

func (fakeBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (fakeBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (fakeBoxes) Remove(context.Context, contract.BoxID) error    { return nil }
func (fakeBoxes) Start(context.Context, contract.BoxID) error     { return nil }
func (fakeBoxes) Stop(context.Context, contract.BoxID) error      { return nil }

func testChannel(t *testing.T, s *Service, ctx context.Context, caller contract.Principal) (<-chan contract.BoxMessage, *contract.Error) {
	t.Helper()
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if current.State == contract.BoxStarting {
		if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
			t.Fatal(problem)
		}
	}
	return s.BoxChannel(ctx, caller)
}

type fakeProfiles struct{}

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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	session, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "worker", Profile: "test", Goal: "goal", Brief: "brief"})
	if e != nil {
		t.Fatal(e)
	}
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
	err = s.store.StartSession(ctx, store.Session{ID: "new-root", Room: "new-room", Name: "root", Profile: "test", Box: current.Box, NewRoomName: "new-room"}, "", "", "", event, contract.Event{}, "")
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
	childAck := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	if _, e := s.ArchiveDone(ctx, childAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: parent}); e != nil {
		t.Fatal(e)
	}
	parentAck := contract.Principal{Kind: contract.PrincipalSession, Session: parent}
	if _, e := s.ArchiveDone(ctx, parentAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	if _, err := s.store.Session(ctx, child.Session); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("child remains open: %v", err)
	}
	if _, err := s.store.Session(ctx, parent); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("parent remains open: %v", err)
	}
}
func TestRoomRepoAPI(t *testing.T) {
	s, room, session, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	agent := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.RoomRepos(ctx, owner, contract.RoomReposRequest{Room: "missing"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("unknown room: %v", e)
	}
	if _, e := s.RoomRepoAdd(ctx, agent, contract.RoomRepoAddRequest{Room: room, Repo: "a/b"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("agent add: %v", e)
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
	if !ok || len(start.Inputs) != 1 || start.Inputs[0].Text != "Goal: goal\n\nbrief" {
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
	if !ok || len(start.Inputs) != 1 || start.Inputs[0].Text != "brief only" {
		t.Fatalf("unexpected brief wake: %+v", start)
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
	if !ok || steer.Queue != contract.QueueSteer || steer.Input.Text != "hello" {
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
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalSession, Session: "child"}, contract.TurnEnded{Session: "child", Outcome: contract.TurnFailed, Error: "tool failed"})
	if err != nil {
		t.Fatal(err)
	}
	d := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: parent}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyPost}, Wake: true}
	if _, err := s.store.Post(ctx, event, []contract.Delivery{d}); err != nil {
		t.Fatal(err)
	}
	steer := (<-channel).Body.(contract.TurnSteer)
	if steer.Input.Text != "turn failed: tool failed" {
		t.Fatalf("text %q", steer.Input.Text)
	}
}

func TestFailedTurnConsumesInputs(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channelCtx, cancel := context.WithCancel(ctx)
	channel, e := testChannel(t, s, channelCtx, caller)
	if e != nil {
		t.Fatal(e)
	}
	start := (<-channel).Body.(contract.TurnStart)
	cancel()
	for range channel {
	}
	if _, e := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnFailed, Error: "setup failed"}); e != nil {
		t.Fatal(e)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, inputs, err := s.store.Dispatch(ctx, session, "retry", event, false)
	if err != nil || turn != "" || len(inputs) != 0 {
		t.Fatalf("failure retried: turn %q inputs %+v error %v", turn, inputs, err)
	}
	var pending int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND state IN ('pending','taken')", session).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unconsumed deliveries %d: %v", pending, err)
	}
	events, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"turn.started"}})
	if e != nil || len(events.Events) != 1 {
		t.Fatalf("started events %+v: %v", events.Events, e)
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
	_, e = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnFailed, Error: "failed", Model: "model"})
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
	if ended.Error != "failed" || ended.Usage.Model != "model" {
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
	if steer.Input.Text != "cross-room" || steer.Input.Reply.Kind != contract.ReplyPost || steer.Input.Reply.Target.Session != sender {
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
	_, inputs, err := s.store.Dispatch(ctx, session, "turn", started, false)
	if err != nil || len(inputs) != 2 || inputs[1].Reply.Kind != contract.ReplyFinal || inputs[1].Reply.Home != home {
		t.Fatalf("post %v inputs %+v: %v", post, inputs, err)
	}
}

func TestOwnPostDoesNotWake(t *testing.T) {
	s, room, session, ctx := setup(t)
	_, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.PostRequest{Room: room, Text: "self", Mentions: []string{"worker"}})
	if e != nil {
		t.Fatal(e)
	}
	turn := contract.TurnID("turn_test")
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	_, deliveries, err := s.store.Dispatch(ctx, session, turn, event, true)
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
	s.boxes = unavailableBoxes{}
	response, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "pending", Profile: "test"})
	if e != nil || response.Session == "" || response.Box == "" {
		t.Fatalf("got %+v, %v", response, e)
	}
	list, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"box.changed"}})
	if e != nil {
		t.Fatal(e)
	}
	last := list.Events[len(list.Events)-1].Body.(contract.BoxChanged)
	if last.Session != response.Session || last.To != contract.BoxFailed || last.Reason != "host unavailable" || last.Cause != contract.CauseCreateFailed {
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
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
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

type blockingBoxes struct{ started chan BoxSpec }

func (b blockingBoxes) Create(ctx context.Context, spec BoxSpec) (string, error) {
	b.started <- spec
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingBoxes) Remove(context.Context, contract.BoxID) error { return nil }

func TestCloseCancelsCreatingBox(t *testing.T) {
	s, room, root, ctx := setup(t)
	adapter := blockingBoxes{started: make(chan BoxSpec)}
	s.boxes = adapter
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "blocked", Profile: "test", Goal: "goal"})
		done <- e
	}()
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
	if e := <-done; e != nil {
		t.Fatal(e)
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
	// A restart after the close marked the pending box and before it finished leaves this state behind.
	if _, err := s.transition(ctx, record.Room, current, contract.BoxClosing, "", ""); err != nil {
		t.Fatal(err)
	}
	if e := s.ResumeClosures(ctx); e != nil {
		t.Fatal(e)
	}
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
	if _, e := s.ArchiveDone(ctx, caller, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
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
	start := func(parent contract.SessionID, name string) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent, Goal: "goal"})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	lead := start(root, "lead")
	if err := s.store.AddHost(ctx, box.Host{Name: "larger", State: box.HostReady, CPUs: 64, MemBytes: 128 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	child := start(lead.Session, "child")
	grandchild := start(child.Session, "grandchild")
	for i, ids := range [][]contract.BoxID{{lead.Box}, {lead.Box, child.Box}, {lead.Box, child.Box, grandchild.Box}} {
		want, err := box.SliceName(ids)
		if err != nil {
			t.Fatal(err)
		}
		if recorder.specs[i].Slice != want || recorder.specs[i].Host != "host" {
			t.Fatalf("placement %d: %+v, want %s on host", i, recorder.specs[i], want)
		}
	}
}

func TestChildWaitsForParentBox(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.boxes = unavailableBoxes{}
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: lead.Session, Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	b, err := s.store.Box(ctx, child.Box)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != contract.BoxPending || b.Reason != string(box.PendingParentHost) {
		t.Fatalf("unexpected box: %+v", b)
	}
}

type heldParentBox struct {
	started  chan BoxSpec
	release  chan struct{}
	children chan BoxSpec
	blocked  contract.BoxID
	unblock  chan struct{}
}

func (b *heldParentBox) Create(_ context.Context, spec BoxSpec) (string, error) {
	select {
	case b.started <- spec:
		<-b.release
	default:
		b.children <- spec
		if spec.Box == b.blocked {
			<-b.unblock
		}
	}
	return "image", nil
}
func (*heldParentBox) Remove(context.Context, contract.BoxID) error { return nil }

func TestPendingChildrenRetryIndependently(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetRepositories(placementRepos{})
	adapter := &heldParentBox{started: make(chan BoxSpec), release: make(chan struct{}), children: make(chan BoxSpec, 2), unblock: make(chan struct{})}
	s.boxes = adapter
	result := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
		result <- e
	}()
	parent := <-adapter.started
	start := func(name string) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent.Session, Goal: "goal"})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	a, b := start("a"), start("b")
	adapter.blocked = a.Box
	close(adapter.release)
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		spec := <-adapter.children
		want, err := box.SliceName([]contract.BoxID{parent.Box, spec.Box})
		if err != nil || spec.Slice != want || spec.Host != parent.Host || len(spec.Repos) != 1 || spec.Repos[0].Name != "owner/repo" {
			t.Fatalf("placement: %+v", spec)
		}
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: parent.Session}); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("close while child starts: %v", e)
	}
	close(adapter.unblock)
	connectAgent(ctx, t, s, parent.Session)
	for _, id := range []contract.SessionID{a.Session, b.Session} {
		unlock := s.lockBox(id)
		unlock()
		connectAgent(ctx, t, s, id)
	}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: parent.Session}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []contract.SessionID{a.Session, b.Session} {
		ack := contract.Principal{Kind: contract.PrincipalSession, Session: id}
		if _, e := s.ArchiveDone(ctx, ack, contract.ArchiveDoneRequest{}); e != nil {
			t.Fatal(e)
		}
	}
	parentAck := contract.Principal{Kind: contract.PrincipalSession, Session: parent.Session}
	if _, e := s.ArchiveDone(ctx, parentAck, contract.ArchiveDoneRequest{}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []contract.SessionID{a.Session, b.Session, parent.Session} {
		if _, err := s.store.Session(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("session remains open: %v", err)
		}
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
	started := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root, Goal: "goal"})
		started <- e
	}()
	parent := <-adapter.started
	closed := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: parent.Session})
		closed <- e
	}()
	<-adapter.cancelled
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: parent.Session, Goal: "goal"})
	if e == nil {
		t.Error("child started while parent was closing")
	}
	close(adapter.release)
	if e := <-started; e != nil {
		t.Fatal(e)
	}
	if e := <-closed; e != nil {
		t.Fatal(e)
	}
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
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: lead.Session}); e != nil {
		t.Fatal(e)
	}
	ack := contract.Principal{Kind: contract.PrincipalSession, Session: lead.Session}
	result := make(chan *contract.Error, 2)
	ackLead := func() {
		_, e := s.ArchiveDone(ctx, ack, contract.ArchiveDoneRequest{})
		result <- e
	}
	go ackLead()
	<-adapter.started
	go ackLead()
	close(adapter.release)
	<-result
	<-result
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
	ended := contract.ShellJobEndedRequest{Job: "job_1", ExitCode: 2, Tail: "boom"}
	for range 2 { // the box agent retries after a lost reply
		if _, e = s.ShellJobEnded(ctx, caller, ended); e != nil {
			t.Fatal(e)
		}
	}
	steer, ok := (<-channel).Body.(contract.TurnSteer)
	if !ok || steer.Queue != contract.QueueSteer || steer.Input.Text != "Shell job job_1 ended with exit code 2. Last output:\nboom" {
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
	s.SetFilesDir(t.TempDir())
	_, e := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{
		Room: room, Text: "hello", Attachments: []contract.Attachment{{Name: "missing", Media: "text/plain", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}},
	})
	if e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("got %v", e)
	}
}

// connectAgent marks the session's box as connected, as a box hello does, and records a turn, so its close waits for an archive ack.
func connectAgent(ctx context.Context, t *testing.T, s *Service, session contract.SessionID) {
	t.Helper()
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
