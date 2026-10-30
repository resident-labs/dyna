package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type fakeBoxes struct{}

func (fakeBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (fakeBoxes) Remove(context.Context, contract.BoxID) error    { return nil }

type fakeProfiles struct{}

func (fakeProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "model", Thinking: contract.ThinkingLow}, []string{"model"}, nil
}

func setup(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	now := func() time.Time { return time.Unix(1000, 0) }
	dir := t.TempDir()
	st, err := store.Open(dir, now)
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
	room, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room"})
	if e != nil {
		t.Fatal(e)
	}
	session, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room.Room, Name: "worker", Profile: "test", Goal: "goal", Brief: "brief"})
	if e != nil {
		t.Fatal(e)
	}
	return s, room.Room, session.Session, ctx
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
	if _, e := s.RoomRepoAdd(ctx, owner, contract.RoomRepoAddRequest{Room: room, Repo: "bad"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("invalid add: %v", e)
	}
	if _, e := s.RoomRepoAdd(ctx, owner, contract.RoomRepoAddRequest{Room: room, Repo: "a/b"}); e != nil {
		t.Fatal(e)
	}
	result, e := s.RoomRepos(ctx, owner, contract.RoomReposRequest{Room: room})
	if e != nil || len(result.Repos) != 1 || result.Repos[0].Repo != "a/b" || !result.Repos[0].Default {
		t.Fatalf("repos %+v: %v", result, e)
	}
}

func TestSessionStartCreatesWake(t *testing.T) {
	s, room, session, ctx := setup(t)
	events, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	body, ok := events.Events[0].Body.(contract.SessionStarted)
	if !ok || body.Brief != "brief" || body.Goal != "goal" {
		t.Fatalf("unexpected start: %+v", events.Events[0])
	}
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if e != nil {
		t.Fatal(e)
	}
	start, ok := (<-channel).Body.(contract.TurnStart)
	if !ok || len(start.Inputs) != 1 || start.Inputs[0].Text != "Goal: goal\n\nbrief" {
		t.Fatalf("unexpected wake: %+v", start)
	}
}
func TestMentionSteersRunningTurn(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := s.BoxChannel(ctx, caller)
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
	channel, e := s.BoxChannel(firstCtx, caller)
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
	reconnect, e := s.BoxChannel(ctx, caller)
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
func TestFailedTurnRecordsError(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	channel, e := s.BoxChannel(ctx, caller)
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
	channel, e := s.BoxChannel(ctx, caller)
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
	if last.Session != response.Session || last.To != contract.BoxFailed || last.Reason != "host unavailable" {
		t.Fatalf("unexpected placement: %+v", last)
	}
}

func TestSessionCannotStart(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.SessionStart(ctx, caller, contract.SessionStartRequest{Room: room, Name: "other", Profile: "test"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("start: %v", e)
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
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session}); e != nil {
		t.Fatal(e)
	}
	list, e := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"box.changed"}})
	if e != nil {
		t.Fatal(e)
	}
	want := []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxReady, contract.BoxClosing, contract.BoxDeleted}
	if len(list.Events) != len(want) {
		t.Fatalf("got %d transitions, want %d", len(list.Events), len(want))
	}
	for i, event := range list.Events {
		change := event.Body.(contract.BoxChanged)
		if change.To != want[i] {
			t.Fatalf("transition %d: got %s, want %s", i, change.To, want[i])
		}
	}
}

func TestSessionStartRejectsParentInM1(t *testing.T) {
	s, room, session, ctx := setup(t)
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: session})
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
	s, room, _, ctx := setup(t)
	adapter := blockingBoxes{started: make(chan BoxSpec)}
	s.boxes = adapter
	done := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "blocked", Profile: "test"})
		done <- e
	}()
	spec := <-adapter.started
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

func TestCloseRetriesAfterBoxDeletion(t *testing.T) {
	s, room, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	closeCtx, cancel := context.WithCancel(ctx)
	s.boxes = cancelOnRemove{cancel: cancel}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.SessionClose(closeCtx, owner, contract.SessionCloseRequest{Session: session}); e == nil {
		t.Fatal("expected canceled close")
	}
	if _, e := s.SessionClose(ctx, owner, contract.SessionCloseRequest{Session: session}); e != nil {
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
