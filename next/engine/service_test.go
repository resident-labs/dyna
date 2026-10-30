package engine

import (
	"context"
	"database/sql"
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
func TestFailedResultReachesParent(t *testing.T) {
	s, room, parent, ctx := setup(t)
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent})
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
func TestPostAcrossRooms(t *testing.T) {
	s, source, sender, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: other.Room, Name: "target", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	channel, e := s.BoxChannel(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: target.Session})
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

func TestSessionCannotClose(t *testing.T) {
	s, _, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.SessionClose(ctx, caller, contract.SessionCloseRequest{Session: session}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("close: %v", e)
	}
}

func TestBoxLifecycleEmitsTransitions(t *testing.T) {
	s, room, root, ctx := setup(t)
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root})
	if e != nil {
		t.Fatal(e)
	}
	session := lead.Session
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
	list.Events = list.Events[3:]
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
	s, room, root, ctx := setup(t)
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root})
	if e != nil {
		t.Fatal(e)
	}
	session := lead.Session
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

type recordingBoxes struct{ specs []BoxSpec }

func (b *recordingBoxes) Create(_ context.Context, spec BoxSpec) (string, error) {
	b.specs = append(b.specs, spec)
	return "image", nil
}
func (b *recordingBoxes) Remove(context.Context, contract.BoxID) error { return nil }

func TestChildPlacement(t *testing.T) {
	s, room, root, ctx := setup(t)
	recorder := &recordingBoxes{}
	s.boxes = recorder
	start := func(parent contract.SessionID, name string) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent})
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
	s, room, root, ctx := setup(t)
	s.boxes = unavailableBoxes{}
	lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root})
	if e != nil {
		t.Fatal(e)
	}
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: lead.Session})
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
	s, room, root, ctx := setup(t)
	adapter := &heldParentBox{started: make(chan BoxSpec), release: make(chan struct{}), children: make(chan BoxSpec, 2), unblock: make(chan struct{})}
	s.boxes = adapter
	result := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root})
		result <- e
	}()
	parent := <-adapter.started
	start := func(name string) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Parent: parent.Session})
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
		if err != nil || spec.Slice != want || spec.Host != parent.Host {
			t.Fatalf("placement: %+v", spec)
		}
	}
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: parent.Session}); e == nil || e.Code != contract.ErrHasChildren {
		t.Fatalf("close: %v", e)
	}
	close(adapter.unblock)
	for _, id := range []contract.SessionID{a.Session, b.Session} {
		unlock := s.lockBox(id)
		unlock()
	}
}

func TestCloseRootIsForbidden(t *testing.T) {
	s, _, root, ctx := setup(t)
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: root}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("close root: %v", e)
	}
}

func TestStartRejectsClosingParent(t *testing.T) {
	s, room, root, ctx := setup(t)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closing' WHERE id=?", root)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: root})
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
	s, room, root, ctx := setup(t)
	adapter := closingInterleaveBox{started: make(chan BoxSpec), cancelled: make(chan struct{}), release: make(chan struct{})}
	s.boxes = adapter
	started := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "lead", Profile: "test", Parent: root})
		started <- e
	}()
	parent := <-adapter.started
	closed := make(chan *contract.Error, 1)
	go func() {
		_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: parent.Session})
		closed <- e
	}()
	<-adapter.cancelled
	_, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: parent.Session})
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
