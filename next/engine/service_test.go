package engine

import (
	"context"
	"database/sql"
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

// ExecArgv echoes the command it would run in the box, so a test can read what the engine asked of it.
func (fakeBoxes) ExecArgv(_ context.Context, _ contract.HostName, id contract.BoxID, argv ...string) ([]string, error) {
	return append([]string{"echo", string(id)}, argv...), nil
}

func (fakeBoxes) Recreate(context.Context, BoxSpec, string) error { return nil }

func (fakeBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (fakeBoxes) Remove(context.Context, contract.BoxID) error    { return nil }
func (fakeBoxes) RemoveFailedBox(context.Context, box.Box) error  { return nil }
func (fakeBoxes) DiscardHostBox(context.Context, box.Box) error   { return nil }
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
	channel, problem := s.BoxChannel(ctx, caller, "")
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

func (fakeProfiles) TextOnlyModels() []contract.TextOnlyModel { return nil }
func (fakeProfiles) List() []contract.Profile                 { return nil }

func (fakeProfiles) CompactionTokens(contract.ProfileName) int { return 0 }

func (fakeProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "model", Thinking: contract.ThinkingLow}, []string{"model"}, nil
}

// setupNow is the clock of setup's service.
func setupNow() time.Time { return time.Unix(1000, 0) }

func setup(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return setupStore(t, st)
}

// setupStore makes setup's host, room and session in st, which the caller closes.
func setupStore(t *testing.T, st *store.Store) (*Service, contract.RoomID, contract.SessionID, context.Context) {
	t.Helper()
	now := setupNow
	if err := st.AddHost(context.Background(), box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now()}); err != nil {
		t.Fatal(err)
	}
	s := New(st, fakeBoxes{}, fakeProfiles{}, now)
	s.SetHostPromoter(func(context.Context, contract.HostName, bool) error { return nil })
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

// A steer that reached the box or Pi after the turn's last model request began was never read. The turn's end must not mark
// it read: it starts the next turn, once, and a steer the model read starts none.
func TestSteerNoRequestReadStartsTheNextTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  contract.TurnOutcome
		cause    contract.FailureCause
		done     bool // the box reported the steer done: Pi's session file holds it
		unread   bool // the box named the steer in unread: its files were still on the way
		state    string
		wake     bool
		nextTurn bool
	}{
		{"never read", contract.TurnCompleted, "", false, false, "pending", true, true},
		{"read", contract.TurnCompleted, "", true, false, "read", true, false},
		{"files on the way", contract.TurnCompleted, "", false, true, "pending", true, true},
		{"stopped", contract.TurnStopped, "", false, false, "pending", true, true},
		{"failed", contract.TurnFailed, contract.CauseModelFailed, false, false, "pending", true, true},
		{"not started", contract.TurnStopped, contract.CauseNotStarted, false, false, "pending", true, true},
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
			if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: start.Inputs[0].Delivery}); problem != nil {
				t.Fatal(problem)
			}
			if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Text: "late", Mentions: []string{"worker"}}); problem != nil {
				t.Fatal(problem)
			}
			steer := (<-channel).Body.(contract.TurnSteer)
			cancel()
			for range channel {
			}
			if tc.done {
				if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: steer.Input.Delivery}); problem != nil {
					t.Fatal(problem)
				}
			}
			output := contract.TurnOutputRequest{Turn: start.Turn, Outcome: tc.outcome, Cause: tc.cause, Final: "about something else"}
			if tc.unread {
				output.Unread = []contract.DeliverySeq{steer.Input.Delivery}
			}
			if _, problem := s.TurnOutput(ctx, caller, output); problem != nil {
				t.Fatal(problem)
			}
			var state string
			var wake bool
			if err := s.store.Read().QueryRowContext(ctx, "SELECT state,wake FROM deliveries WHERE to_id=? AND seq=?", session, steer.Input.Delivery).Scan(&state, &wake); err != nil {
				t.Fatal(err)
			}
			if state != tc.state || wake != tc.wake {
				t.Fatalf("steer state=%s wake=%v, want %s %v", state, wake, tc.state, tc.wake)
			}
			if state == "pending" {
				// A done that comes after the turn ended finds nothing to mark, so the next turn still has the steer, and the
				// box, finding it in Pi's session file, gives Pi nothing twice.
				if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: steer.Input.Delivery}); problem == nil {
					t.Fatal("late done marked a pending delivery")
				}
			}
			event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				t.Fatal(err)
			}
			turn, inputs, _, err := s.store.Dispatch(ctx, session, "next", event, false)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.nextTurn {
				if turn != "" {
					t.Fatalf("extra turn %q with %+v", turn, inputs)
				}
				return
			}
			wantInputs := 1
			if tc.outcome == contract.TurnFailed || tc.cause == contract.CauseNotStarted {
				wantInputs++ // the failed attempt's original input is returned without a wake
			}
			if turn != "next" || len(inputs) != wantInputs || inputs[len(inputs)-1].Seq != steer.Input.Delivery || inputs[len(inputs)-1].Queue != nil {
				t.Fatalf("next turn=%q inputs=%+v", turn, inputs)
			}
			if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: steer.Input.Delivery}); problem != nil {
				t.Fatal(problem)
			}
			if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "answered"}); problem != nil {
				t.Fatal(problem)
			}
			event, err = s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
			if err != nil {
				t.Fatal(err)
			}
			if turn, inputs, _, err := s.store.Dispatch(ctx, session, "again", event, false); err != nil || turn != "" {
				t.Fatalf("a third turn: %q %+v %v", turn, inputs, err)
			}
		})
	}
}

func TestPostAcrossRooms(t *testing.T) {
	s, source, _, ctx := setup(t)
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
	// Only the owner reaches another room: a session's mentions stay in its chain of command.
	_, e = s.Post(ctx, owner, contract.PostRequest{Room: source, Text: "cross-room", Mentions: []string{"@other:target"}})
	if e != nil {
		t.Fatal(e)
	}
	steer := (<-channel).Body.(contract.TurnSteer)
	if inputBody(steer.Input.Text) != "cross-room" || steer.Input.Reply.Kind != contract.ReplyFinal || steer.Input.Reply.Target.Kind != contract.PrincipalOwner {
		t.Fatalf("got %+v", steer)
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

func TestSessionCannotClose(t *testing.T) {
	s, _, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	if _, e := s.SessionClose(ctx, caller, contract.SessionCloseRequest{Session: session}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("close: %v", e)
	}
}

func TestCloseRootIsForbidden(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: root}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("close root: %v", e)
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
	ended := contract.ShellJobEndedRequest{Job: "job_1", Description: "Build the bundle", End: &contract.ShellJobEnd{Kind: contract.ShellJobSignaled, Signal: "SIGTERM"}, Tail: "boom"}
	for range 2 { // the box agent retries after a lost reply
		if _, e = s.ShellJobEnded(ctx, caller, ended); e != nil {
			t.Fatal(e)
		}
	}
	steer, ok := (<-channel).Body.(contract.TurnSteer)
	if !ok || steer.Queue != contract.QueueSteer || inputBody(steer.Input.Text) != "Your shell job 'Build the bundle' ended by a signal (SIGTERM). Last output:\nboom" {
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

// jobExited is the End of a job that exited cleanly.
var jobExited = &contract.ShellJobEnd{Kind: contract.ShellJobExited}

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
