package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
)

// claimEngine is the request shell in front of RunnerClaimInput. A retry
// returns the receipt saved by the claim, as requestRunner does. The engine
// method selects and claims the posts.
type claimEngine struct {
	sessionID string
	dataDir   string
	m         *Manager
	publish   func(hub.Event)
	client    *engineclient.Client

	mu          sync.Mutex
	w           *state.Writer
	firstWriter *state.Writer
	retryWriter *state.Writer
	calls       []string
	claims      int
	waitCalls   int
	waitFault   int
	replayed    int
	reopened    bool
	cached      []byte
	hold        *responseHold
	dropOnce    bool
	dropped     bool
	fault       error

	srv *http.Server
	ln  net.Listener
}

type responseHold struct {
	committed chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (h *responseHold) Release() {
	if h == nil {
		return
	}
	h.once.Do(func() { close(h.release) })
}

type eventLog struct {
	mu sync.Mutex
	ev []hub.Event
}

func (l *eventLog) add(ev hub.Event) {
	l.mu.Lock()
	l.ev = append(l.ev, ev)
	l.mu.Unlock()
}

func (l *eventLog) members(session string) int {
	return l.count("members", session)
}

func (l *eventLog) count(topic, session string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, ev := range l.ev {
		if ev.Topic == topic && ev.Key == session {
			n++
		}
	}
	return n
}

type claimFixture struct {
	e       *testEnv
	info    Info
	msg     room.Message
	dir     string
	dataDir string
	events  *eventLog
}

// claimedTurn is the turn the fixture session's row for postID names, or 0
// when no turn holds it.
func (f *claimFixture) claimedTurn(t *testing.T, postID string) int {
	t.Helper()
	return claimsOf(t, f.e, f.info.ID).claimedTurn[postID]
}

func newClaimFixture(t *testing.T, text string) *claimFixture {
	t.Helper()
	e := newTestEnv(t)
	e.m.InputEvents = hub.New(1)
	info := completedFixtureSession(t, e)
	msg := postClaimInput(t, e, info, text)
	dataDir := filepath.Dir(e.m.Store.root())
	if err := state.MigrateRequests(dataDir); err != nil {
		t.Fatal(err)
	}
	dir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.rooms().SessionDeliveryRows(info.RoomID, room.DeliveryKeys(info.ID, info.Address())); err != nil {
		t.Fatal(err)
	}
	events := &eventLog{}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	state.For(db).SetPublish(events.add)
	return &claimFixture{e: e, info: info, msg: msg, dir: dir, dataDir: dataDir, events: events}
}

func (f *claimFixture) serve(t *testing.T) *claimEngine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	eng := &claimEngine{
		sessionID: f.info.ID,
		dataDir:   f.dataDir,
		m:         f.e.m,
		publish:   f.events.add,
		client:    engineclient.New(path, engineclient.Notices(io.Discard), engineclient.RestartWait(200*time.Millisecond)),
		ln:        ln,
	}
	db, err := database.Open(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	eng.install(db)
	eng.srv = &http.Server{Handler: eng}
	go eng.srv.Serve(ln)
	f.e.m.Engine = eng.client
	return eng
}

func (f *claimFixture) openGate(t *testing.T, clock *liveClock) *turnGate {
	t.Helper()
	meta, err := f.e.m.Store.ReadMeta(f.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate := f.e.m.Store.newTurnGate(f.e.m.rooms(), meta, 1, meta.RoomCursor)
	gate.writes = f.e.m.runnerWrites(f.info.ID, 1)
	gate.liveDeadline = clock.deadline
	if err := gate.Activate(); err != nil {
		t.Fatal(err)
	}
	return gate
}

func (s *claimEngine) install(db *sql.DB) {
	w := state.For(db)
	if s.publish != nil {
		w.SetPublish(s.publish)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstWriter == nil {
		s.firstWriter = w
	}
	s.w = w
}

func (s *claimEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/RunnerTurnNote" {
		var args []TurnNote
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 {
			writeRemote(w, http.StatusBadRequest, errors.New("note needs one argument"))
			return
		}
		actor := authority.WithActor(r.Context(), authority.Actor{Role: authority.Session, Session: s.sessionID})
		rid := r.Header.Get(callspec.RequestHeader)
		call := state.ForRequest(callspec.WithRequest(actor, rid), "runner/"+rid)
		if err := s.m.RunnerTurnNote(call, args[0]); err != nil {
			writeRemote(w, http.StatusBadRequest, err)
			return
		}
		_, _ = w.Write([]byte("null"))
		return
	}
	if r.URL.Path == "/api/RunnerWaitInput" {
		s.mu.Lock()
		s.waitCalls++
		fault := s.waitFault
		s.waitFault = 0
		s.mu.Unlock()
		if fault != 0 {
			if fault < 0 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			} else {
				writeRemote(w, fault, errors.New("temporary wait outage"))
			}
			return
		}
		if r.Header.Get(callspec.RequestHeader) != "" {
			writeRemote(w, http.StatusBadRequest, errors.New("wait carried a request ID"))
			return
		}

		var args []InputClaim
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 {
			writeRemote(w, http.StatusBadRequest, errors.New("wait needs one input argument"))
			return
		}
		actor := authority.WithActor(r.Context(), authority.Actor{Role: authority.Session, Session: s.sessionID})
		ready, err := s.m.RunnerWaitInput(actor, args[0])
		if err != nil {
			writeRemote(w, http.StatusBadRequest, err)
			return
		}
		_ = json.NewEncoder(w).Encode(ready)
		return
	}
	if r.URL.Path != "/api/RunnerClaimInput" {
		// 404 would make the outbox run the local fallback and claim twice.
		writeRemote(w, http.StatusBadRequest, errors.New("this engine only claims runner input"))
		return
	}
	rid := r.Header.Get(callspec.RequestHeader)
	s.mu.Lock()
	s.calls = append(s.calls, rid)
	writer := s.w
	s.mu.Unlock()
	if rid == "" || writer == nil {
		writeRemote(w, http.StatusBadRequest, errors.New("runner claim needs a request ID"))
		return
	}
	key := "runner/" + rid
	actor := authority.WithActor(r.Context(), authority.Actor{Role: authority.Session, Session: s.sessionID})
	reqCtx := state.ForRequestCall(callspec.WithRequest(actor, rid), key, "RunnerClaimInput", []byte("fixture-call"))
	if result, done, err := writer.RequestDone(reqCtx, key); err != nil || done {
		s.finishReplay(w, result, err)
		return
	}
	var args []json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) != 1 {
		writeRemote(w, http.StatusBadRequest, errors.New("runner claim needs one argument"))
		return
	}
	var input InputClaim
	if err := json.Unmarshal(args[0], &input); err != nil {
		writeRemote(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	s.claims++
	hold := s.hold
	s.hold = nil
	drop := s.dropOnce && !s.dropped
	if drop {
		s.dropped = true
	}
	s.mu.Unlock()
	reply, err := s.m.RunnerClaimInput(reqCtx, input)
	if errors.Is(err, state.ErrRepeated) {
		result, _, doneErr := writer.RequestDone(reqCtx, key)
		s.finishReplay(w, result, doneErr)
		return
	}
	if err != nil {
		s.note(err)
		if hold != nil {
			close(hold.committed)
		}
		writeRemote(w, http.StatusBadRequest, err)
		return
	}
	if drop {
		if err := s.reopen(); err != nil {
			s.note(err)
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			s.note(errors.New("claim connection cannot be dropped"))
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			s.note(err)
			return
		}
		_ = conn.Close()
		return
	}
	body, err := json.Marshal(reply)
	if err != nil {
		s.note(err)
		writeRemote(w, http.StatusBadRequest, err)
		return
	}
	if hold != nil {
		close(hold.committed)
		<-hold.release
	}
	_, _ = w.Write(body)
}

func (s *claimEngine) finishReplay(w http.ResponseWriter, result []byte, err error) {
	if err != nil {
		s.note(err)
		writeRemote(w, http.StatusBadRequest, err)
		return
	}
	body := result
	if len(body) == 0 {
		body = []byte("null")
	}
	s.mu.Lock()
	s.replayed++
	s.cached = append([]byte(nil), body...)
	s.mu.Unlock()
	_, _ = w.Write(body)
}

func (s *claimEngine) reopen() error {
	if err := database.Close(s.dataDir); err != nil {
		return err
	}
	db, err := database.Open(s.dataDir)
	if err != nil {
		return err
	}
	w := state.For(db)
	if s.publish != nil {
		w.SetPublish(s.publish)
	}
	s.mu.Lock()
	s.w = w
	s.retryWriter = w
	s.reopened = true
	s.mu.Unlock()
	return nil
}

func (s *claimEngine) note(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.fault == nil {
		s.fault = err
	}
	s.mu.Unlock()
}

func (s *claimEngine) faultNow() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fault
}

func (s *claimEngine) snapshot() (calls []string, claims, replayed int, cached []byte, reopened bool, first, retry *state.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...), s.claims, s.replayed, append([]byte(nil), s.cached...), s.reopened, s.firstWriter, s.retryWriter
}

func (s *claimEngine) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s.srv != nil {
		_ = s.srv.Shutdown(ctx)
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func writeRemote(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

type liveClock struct {
	ch chan time.Time
}

func newLiveClock() *liveClock {
	return &liveClock{ch: make(chan time.Time, 1)}
}

func (c *liveClock) deadline() (<-chan time.Time, func()) {
	return c.ch, func() {}
}

func (c *liveClock) fire() {
	c.ch <- time.Now()
}

type readResult struct {
	msgs   []runstore.SteeringMessage
	cursor int64
	err    error
}

func readGate(gate *turnGate, final bool) <-chan readResult {
	out := make(chan readResult, 1)
	go func() {
		msgs, cursor, err := gate.Read(0, final)
		out <- readResult{msgs: msgs, cursor: cursor, err: err}
	}()
	return out
}

func waitCh(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", what)
	}
}

// selectCtx reports when Read's select queries Done. The call returns the
// underlying context's channel, so the select still waits on that context.
type selectCtx struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *selectCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func countSteer(msgs []runstore.SteeringMessage, id string) int {
	n := 0
	for _, msg := range msgs {
		if msg.ID == id {
			n++
		}
	}
	return n
}

func sameClaim(a, b ClaimedInput) bool {
	if a.Cursor != b.Cursor || a.Depth != b.Depth || a.ParentSession != b.ParentSession || a.Closed != b.Closed || len(a.Posts) != len(b.Posts) {
		return false
	}
	for i := range a.Posts {
		if a.Posts[i] != b.Posts[i] {
			return false
		}
	}
	return true
}

// Cleanup must not derive its deadline from t.Context: that context is already
// canceled when cleanup runs, and Close would give up on a live outbox.
func cleanupRemote(release func(), outs []*engineclient.Outbox, eng *claimEngine) {
	if release != nil {
		release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, out := range outs {
		if out != nil {
			out.Close(ctx)
		}
	}
	if eng != nil {
		eng.close()
	}
}

func TestLiveReadFailsOpenWhileOneClaimIsUnresolved(t *testing.T) {
	fix := newClaimFixture(t, "live once")
	eng := fix.serve(t)
	hold := &responseHold{committed: make(chan struct{}), release: make(chan struct{})}
	eng.hold = hold
	clock := newLiveClock()
	gate := fix.openGate(t, clock)
	var outs []*engineclient.Outbox
	outs = append(outs, gate.writes.out)
	t.Cleanup(func() { gate.Deactivate(); cleanupRemote(hold.Release, outs, eng) })

	before := gate.cursor
	first := readGate(gate, false)
	waitCh(t, "the live claim to commit", hold.committed)
	if err := eng.faultNow(); err != nil {
		t.Fatal(err)
	}
	clock.fire()
	got := <-first
	if got.err != nil || len(got.msgs) != 0 || got.cursor != before {
		t.Fatalf("fail-open read = %+v cursor %d, %v; want no posts and cursor %d", got.msgs, got.cursor, got.err, before)
	}
	if gate.pending == nil || gate.cursor != before {
		t.Fatalf("pending claim = %v, cursor %d; want the same unresolved call at %d", gate.pending != nil, gate.cursor, before)
	}
	calls, claims, _, _, _, _, _ := eng.snapshot()
	if len(calls) != 1 || claims != 1 {
		t.Fatalf("requests while unresolved = %v, claims %d; want one call", calls, claims)
	}
	st := readGateState(fix.dir)
	if !st.Active || st.ClosedAt != 0 {
		t.Fatalf("gate after fail-open = %+v; want it still open", st)
	}

	hold.Release()
	second := <-readGate(gate, false)
	if second.err != nil || countSteer(second.msgs, fix.msg.ID) != 1 || len(second.msgs) != 1 || second.cursor == before {
		t.Fatalf("consuming read = %+v cursor %d, %v; want %s once", second.msgs, second.cursor, second.err, fix.msg.ID)
	}
	calls, claims, _, _, _, _, _ = eng.snapshot()
	if len(calls) != 1 || claims != 1 {
		t.Fatalf("requests after consume = %v, claims %d; want the original call only", calls, claims)
	}
	later := readGate(gate, false)
	clock.fire()
	third := awaitInput(t, later)
	if third.err != nil || countSteer(third.msgs, fix.msg.ID) != 0 {
		t.Fatalf("later read = %+v, %v; want the accepted post to stay consumed", third.msgs, third.err)
	}
	turn := fix.claimedTurn(t, fix.msg.ID)
	calls, _, _, _, _, _, _ = eng.snapshot()
	if turn != 1 || len(calls) != 1 || gate.waiting == nil {
		t.Fatalf("claim turn %d, requests %v; want one claim and a retained read-only wait", turn, calls)
	}
}

func TestClaimRetryAfterRestartReturnsTheCachedBatchOnce(t *testing.T) {
	fix := newClaimFixture(t, "accepted once")
	eng := fix.serve(t)
	eng.dropOnce = true
	writes := fix.e.m.runnerWrites(fix.info.ID, 1)
	t.Cleanup(func() { cleanupRemote(nil, []*engineclient.Outbox{writes.out}, eng) })

	before := fix.events.members(fix.info.ID)
	reply, err := writes.claimInput(context.Background(), InputClaim{Turn: 1, Startup: true})
	if err != nil {
		t.Fatal(err)
	}
	if fault := eng.faultNow(); fault != nil {
		t.Fatal(fault)
	}
	calls, claims, replayed, cached, reopened, first, retry := eng.snapshot()
	if !reopened || first == nil || retry == nil || first == retry {
		t.Fatalf("restart writers reopened=%v first=%p retry=%p; want a fresh writer", reopened, first, retry)
	}
	if len(calls) != 2 || calls[0] == "" || calls[0] != calls[1] || claims != 1 || replayed != 1 {
		t.Fatalf("requests %v, claims %d, replays %d; want one claim and one replay of that ID", calls, claims, replayed)
	}
	if len(cached) == 0 || bytes.Equal(cached, []byte("null")) {
		t.Fatalf("cached reply = %q; want the committed JSON", cached)
	}
	var want ClaimedInput
	if err := json.Unmarshal(cached, &want); err != nil {
		t.Fatal(err)
	}
	if !sameClaim(reply, want) || len(reply.Posts) != 1 || reply.Posts[0].Steer.ID != fix.msg.ID {
		t.Fatalf("batch %+v; cached %s", reply, cached)
	}
	db, err := database.Open(fix.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	saved, done, err := state.For(db).RequestDone(context.Background(), "runner/"+calls[0])
	if err != nil || !done || !bytes.Equal(saved, cached) {
		t.Fatalf("reopened receipt = %q done %v, %v; want %s", saved, done, err, cached)
	}
	if turn := fix.claimedTurn(t, fix.msg.ID); turn != 1 || fix.events.members(fix.info.ID)-before != 0 {
		t.Fatalf("claim turn %d, events %d; want one claim and no interim members event", turn, fix.events.members(fix.info.ID)-before)
	}
}

func TestFinalReadDoesNotSealWhileClaimIsUnresolved(t *testing.T) {
	fix := newClaimFixture(t, "seal after delivery")
	eng := fix.serve(t)
	hold := &responseHold{committed: make(chan struct{}), release: make(chan struct{})}
	eng.hold = hold
	clock := newLiveClock()
	gate := fix.openGate(t, clock)
	var outs []*engineclient.Outbox
	outs = append(outs, gate.writes.out)
	t.Cleanup(func() { gate.Deactivate(); cleanupRemote(hold.Release, outs, eng) })

	live := readGate(gate, false)
	waitCh(t, "the live claim to commit", hold.committed)
	if err := eng.faultNow(); err != nil {
		t.Fatal(err)
	}
	clock.fire()
	if got := <-live; got.err != nil || len(got.msgs) != 0 {
		t.Fatalf("fail-open read = %+v, %v; want no posts yet", got.msgs, got.err)
	}

	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watch := &selectCtx{Context: base, entered: make(chan struct{})}
	gate.ctx = watch
	final := readGate(gate, true)
	waitCh(t, "the final read to query its context", watch.entered)
	gate.writes.terminalMu.Lock()
	armed := gate.writes.terminal != nil
	gate.writes.terminalMu.Unlock()
	if !armed {
		t.Fatal("the final input wait did not arm its terminal scope")
	}
	select {
	case got := <-final:
		t.Fatalf("final read returned while the claim was unresolved: %+v", got)
	default:
	}
	if st := readGateState(fix.dir); !st.Active {
		t.Fatalf("gate sealed while the claim was unresolved: %+v", st)
	}
	calls, claims, _, _, _, _, _ := eng.snapshot()
	if len(calls) != 1 || claims != 1 || gate.pending == nil {
		t.Fatalf("requests %v, claims %d, pending %v; want the one unresolved call", calls, claims, gate.pending != nil)
	}

	hold.Release()
	got := <-final
	if got.err != nil || countSteer(got.msgs, fix.msg.ID) != 1 || len(got.msgs) != 1 {
		t.Fatalf("final read = %+v, %v; want %s once", got.msgs, got.err, fix.msg.ID)
	}
	if st := readGateState(fix.dir); !st.Active {
		t.Fatalf("gate sealed when the final read still had input: %+v", st)
	}
	sealedMsgs, sealedCursor, err := gate.Read(0, true)
	st := readGateState(fix.dir)
	if err != nil || len(sealedMsgs) != 0 || st.Active || st.ClosedAt != sealedCursor {
		t.Fatalf("empty final read = %+v cursor %d, gate %+v, %v; want a seal at that cursor", sealedMsgs, sealedCursor, st, err)
	}
}

func TestLateClaimReplyDoesNotAdvanceOldGateOrLoseInput(t *testing.T) {
	fix := newClaimFixture(t, "recover me")
	eng := fix.serve(t)
	hold := &responseHold{committed: make(chan struct{}), release: make(chan struct{})}
	eng.hold = hold
	clock := newLiveClock()
	gate := fix.openGate(t, clock)
	var outs []*engineclient.Outbox
	outs = append(outs, gate.writes.out)
	t.Cleanup(func() { gate.Deactivate(); cleanupRemote(hold.Release, outs, eng) })

	live := readGate(gate, false)
	waitCh(t, "the live claim to commit", hold.committed)
	if err := eng.faultNow(); err != nil {
		t.Fatal(err)
	}
	clock.fire()
	got := <-live
	if got.err != nil || len(got.msgs) != 0 || got.cursor != gate.cursor {
		t.Fatalf("fail-open read = %+v cursor %d, %v; want cursor %d", got.msgs, got.cursor, got.err, gate.cursor)
	}
	cursor := gate.cursor
	ended := time.Now()
	if _, err := fix.e.m.Store.UpdateTurn(fix.info.ID, 1, func(turn *Turn) {
		turn.Status = TurnOK
		turn.Ended = ended
	}); err != nil {
		t.Fatal(err)
	}
	gate.Deactivate()
	closed := readGateState(fix.dir)
	if closed.Active || closed.ClosedAt != cursor {
		t.Fatalf("deactivated gate = %+v; want closed at %d", closed, cursor)
	}

	if gate.pending == nil {
		t.Fatal("deactivated gate lost its pending claim")
	}
	hold.Release()
	// The reply is the settlement event. This gate is not read again, so the
	// cursor and gate file stay where the turn ended.
	var settled inputReply
	timer := time.NewTimer(30 * time.Second)
	select {
	case settled = <-gate.pending.done:
	case <-timer.C:
		t.Fatal("timed out waiting for the late reply to settle")
	}
	timer.Stop()
	if settled.err != nil || len(settled.input.Posts) != 1 || settled.input.Posts[0].Steer.ID != fix.msg.ID || settled.input.Cursor == cursor {
		t.Fatalf("settled reply = %+v, %v; want %s past cursor %d", settled.input, settled.err, fix.msg.ID, cursor)
	}
	if gate.cursor != cursor || readGateState(fix.dir) != closed {
		t.Fatalf("after settlement cursor %d gate %+v; want cursor %d and %+v", gate.cursor, readGateState(fix.dir), cursor, closed)
	}

	now := time.Now()
	if err := fix.e.m.Store.writeTurn(fix.info.ID, Turn{N: 2, Status: TurnQueued, Profile: "mock", Created: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := fix.e.m.Store.UpdateMeta(fix.info.ID, func(meta *Meta) { meta.Turns = 2 }); err != nil {
		t.Fatal(err)
	}
	next := fix.e.m.runnerWrites(fix.info.ID, 2)
	outs = append(outs, next.out)
	reply, err := next.claimInput(context.Background(), InputClaim{Turn: 2, Startup: true})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, post := range reply.Posts {
		if post.Steer.ID == fix.msg.ID {
			n++
		}
	}
	if n != 1 || len(reply.Posts) != 1 {
		t.Fatalf("startup recovery = %+v; want %s once", reply.Posts, fix.msg.ID)
	}
	if turn := fix.claimedTurn(t, fix.msg.ID); turn != 2 || gate.cursor != cursor || readGateState(fix.dir) != closed {
		t.Fatalf("claim turn %d, old cursor %d, gate %+v; want turn 2 and the old gate unchanged", turn, gate.cursor, readGateState(fix.dir))
	}
}

func TestFinalClaimExpiryDoesNotSealOrAdvanceInput(t *testing.T) {
	fix := newClaimFixture(t, "recover after expiry")
	eng := fix.serve(t)
	hold := &responseHold{committed: make(chan struct{}), release: make(chan struct{})}
	eng.hold = hold
	gate := fix.openGate(t, newLiveClock())
	t.Cleanup(func() { cleanupRemote(hold.Release, []*engineclient.Outbox{gate.writes.out}, eng) })
	spool := gate.writes.out.Spool
	var saved []engineclient.Queued
	gate.writes.out.Spool = func(q []engineclient.Queued) error {
		saved = append(saved, q...)
		return spool(q)
	}
	before := gate.cursor
	final := readGate(gate, true)
	waitCh(t, "the final claim to commit", hold.committed)
	gate.writes.terminalMu.Lock()
	expire := gate.writes.terminalCancel
	gate.writes.terminalMu.Unlock()
	if expire == nil {
		t.Fatal("final input did not arm terminal persistence")
	}
	expire()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case got := <-final:
		if !errors.Is(got.err, engineclient.ErrSpooled) || len(got.msgs) != 0 || got.cursor != before {
			t.Fatalf("expired final claim = %+v, want failed input at cursor %d", got, before)
		}
	case <-timer.C:
		t.Fatal("expired final claim did not settle")
	}
	if st := readGateState(fix.dir); !st.Active || st.ClosedAt != 0 || gate.cursor != before {
		t.Fatalf("expired claim advanced or sealed input: cursor %d, gate %+v", gate.cursor, st)
	}
	if len(saved) != 1 || saved[0].Method != "RunnerClaimInput" {
		t.Fatalf("spooled claims = %+v, want the unanswered claim with its original ID", saved)
	}
	calls, _, _, _, _, _, _ := eng.snapshot()
	if len(calls) != 1 || calls[0] != saved[0].ID {
		t.Fatalf("spooled request %s differs from the live request %v", saved[0].ID, calls)
	}
}

func TestClaimRollbackDropsReceiptThenRetryCommitsOnce(t *testing.T) {
	fix := newClaimFixture(t, "commit on retry")
	db, err := database.Open(fix.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_delivery_state
BEFORE UPDATE ON post_deliveries
BEGIN
    SELECT RAISE(ABORT, 'reject queue write');
END`); err != nil {
		t.Fatal(err)
	}
	actor := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: fix.info.ID})
	rid := fix.info.ID + "/claim/rollback"
	ctx := state.ForRequestCall(callspec.WithRequest(actor, rid), "runner/"+rid, "RunnerClaimInput", []byte("fixture-call"))
	input := InputClaim{Turn: 1, Startup: true}
	before := fix.events.members(fix.info.ID)
	changesBefore := fix.events.count("sessions", fix.info.ID)
	reply, err := fix.e.m.RunnerClaimInput(ctx, input)
	if err == nil || len(reply.Posts) != 0 {
		t.Fatalf("rejected claim = %+v, %v; want no posts and an error", reply, err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("reject queue write")) {
		t.Fatalf("rejected claim = %v; want the queue trigger", err)
	}
	turn := fix.claimedTurn(t, fix.msg.ID)
	saved, done, doneErr := state.For(db).RequestDone(context.Background(), "runner/"+rid)
	var receipts int
	countErr := db.QueryRow(`SELECT COUNT(*) FROM request_ids WHERE id = ?`, "runner/"+rid).Scan(&receipts)
	if doneErr != nil || countErr != nil || turn != 0 || done || saved != nil || receipts != 0 || fix.events.members(fix.info.ID) != before {
		t.Fatalf("after rollback claim turn %d, receipt %q done %v rows %d events %d, %v %v", turn, saved, done, receipts, fix.events.members(fix.info.ID)-before, doneErr, countErr)
	}
	if got := fix.events.count("sessions", fix.info.ID) - changesBefore; got != 0 {
		t.Fatalf("rollback published %d session changes; want none", got)
	}

	if _, err := db.Exec(`DROP TRIGGER reject_delivery_state`); err != nil {
		t.Fatal(err)
	}
	reply, err = fix.e.m.RunnerClaimInput(ctx, input)
	if err != nil || len(reply.Posts) != 1 || reply.Posts[0].Steer.ID != fix.msg.ID {
		t.Fatalf("retry = %+v, %v; want %s", reply, err, fix.msg.ID)
	}
	turn = fix.claimedTurn(t, fix.msg.ID)
	saved, done, doneErr = state.For(db).RequestDone(context.Background(), "runner/"+rid)
	var stored ClaimedInput
	unmarshalErr := json.Unmarshal(saved, &stored)
	if doneErr != nil || unmarshalErr != nil || turn != 1 || !done || len(saved) == 0 || bytes.Equal(saved, []byte("null")) || !sameClaim(reply, stored) || fix.events.members(fix.info.ID)-before != 0 {
		t.Fatalf("retry claim turn %d, receipt %q events %d, %v %v", turn, saved, fix.events.members(fix.info.ID)-before, doneErr, unmarshalErr)
	}
	if got := fix.events.count("sessions", fix.info.ID) - changesBefore; got != 1 {
		t.Fatalf("committed retry published %d session changes; want one", got)
	}
	replayCtx := state.ForRequestCall(callspec.WithRequest(actor, rid), "runner/"+rid, "RunnerClaimInput", []byte("fixture-call"))
	if _, err := fix.e.m.RunnerClaimInput(replayCtx, input); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replayed claim = %v; want ErrRepeated", err)
	}
	if got := fix.events.count("sessions", fix.info.ID) - changesBefore; got != 1 {
		t.Fatalf("replay published another session change: %d total; want one", got)
	}
}
