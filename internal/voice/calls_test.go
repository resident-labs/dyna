package voice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/voice/realtime"
)

// fakeGateways starts fake calls that the test drives with events. A start
// waits for a value on ready (nil: the call connects) unless hold is false.
// A call ends after its context ends, or when exit gets an error;
// either way it first reports on cancelled and then waits for gate.
type fakeGateways struct {
	hold      bool
	started   chan string // the room of each start
	ready     chan error
	exit      chan error
	cancelled chan string // the room of each gateway that is ending
	gate      chan struct{}
}

func newCalls(t *testing.T, hold bool) (*Calls, *fakeGateways) {
	t.Helper()
	f := &fakeGateways{hold: hold, started: make(chan string, 4), ready: make(chan error), exit: make(chan error, 1), cancelled: make(chan string, 4), gate: make(chan struct{})}
	c := &Calls{
		Ctx:   t.Context(),
		ready: func() string { return "" },
		Open: func(ref string) (*RoomLink, error) {
			return &RoomLink{rec: room.Record{ID: "id-" + ref, Name: ref}}, nil
		},
		start: func(ctx context.Context, link *RoomLink) (*session, error) {
			f.started <- link.Name()
			if f.hold {
				select {
				case err := <-f.ready:
					if err != nil {
						return nil, err
					}
				case <-ctx.Done():
					f.cancelled <- link.Name()
					<-f.gate
					return nil, ctx.Err()
				}
			}
			gw := &session{call: fakeCall{}, done: make(chan struct{})}
			go func() {
				select {
				case <-ctx.Done():
				case gw.err = <-f.exit:
				}
				f.cancelled <- link.Name()
				<-f.gate
				close(gw.done)
			}()
			return gw, nil
		},
	}
	t.Cleanup(func() {
		select {
		case <-f.gate:
		default:
			close(f.gate)
		}
		c.Stop()
	})
	return c, f
}

// fakeCall is a call whose page side does nothing.
type fakeCall struct{}

func (fakeCall) Attach(*realtime.Page) func()  { return func() {} }
func (fakeCall) FromPage(realtime.PageMessage) {}

type started struct {
	st  CallStatus
	err error
}

func startAsync(c *Calls, room string) chan started {
	out := make(chan started, 1)
	go func() {
		st, err := c.Start(room)
		out <- started{st, err}
	}()
	return out
}

func TestCallsStartIsIdempotentForTheLiveRoom(t *testing.T) {
	c, _ := newCalls(t, false)
	before := time.Now().UnixMilli()
	first, err := c.Start("voice-test")
	if err != nil {
		t.Fatal(err)
	}
	// The app shows the call's posts from its start on.
	if first.StartedAt < before || first.StartedAt > time.Now().UnixMilli() {
		t.Fatalf("startedAt %d is not this call's start", first.StartedAt)
	}
	want := CallStatus{Available: true, Live: true, RoomID: "id-voice-test", Room: "voice-test", StartedAt: first.StartedAt}
	if first != want {
		t.Fatalf("status = %+v, want %+v", first, want)
	}
	again, err := c.Start("voice-test")
	if err != nil || again != want {
		t.Fatalf("second start = %+v, %v", again, err)
	}
	if send, leave, err := c.Join(nil); err != nil || send == nil {
		t.Fatalf("join = %v", err)
	} else {
		leave()
	}
}

// One gateway at a time: a call for another room names the live room, so
// the app can offer to end it.
func TestCallsRefuseASecondRoomByName(t *testing.T) {
	c, f := newCalls(t, false)
	close(f.gate)
	if _, err := c.Start("room-a"); err != nil {
		t.Fatal(err)
	}
	_, err := c.Start("room-b")
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Room != "room-a" || !strings.Contains(err.Error(), "room-a") {
		t.Fatalf("err = %v, want a BusyError naming room-a", err)
	}
	c.Stop()
	if st := c.Status(); st.Live || st.Room != "" {
		t.Fatalf("status after stop = %+v", st)
	}
	if _, _, err := c.Join(nil); err == nil {
		t.Fatal("Connect worked with no live call")
	}
	if st, err := c.Start("room-b"); err != nil || st.Room != "room-b" {
		t.Fatalf("start after stop = %+v, %v", st, err)
	}
}

// Starts that overlap a start in progress are decided under the lock: the
// same room is still starting, and another room is busy. Run with -race.
func TestCallsConcurrentStarts(t *testing.T) {
	c, f := newCalls(t, true)
	first := startAsync(c, "room-a")
	<-f.started
	if _, err := c.Start("room-a"); err == nil || !strings.Contains(err.Error(), "still starting") {
		t.Fatalf("same room while starting: %v", err)
	}
	var busy *BusyError
	if _, err := c.Start("room-b"); !errors.As(err, &busy) || busy.Room != "room-a" {
		t.Fatalf("other room while starting: %v", err)
	}
	if st := c.Status(); st.Live || st.Room != "room-a" {
		t.Fatalf("status while starting = %+v", st)
	}
	f.ready <- nil
	if got := <-first; got.err != nil || !got.st.Live || got.st.Room != "room-a" {
		t.Fatalf("first start = %+v", got)
	}
}

// Stop during a start keeps the call reserved until the start has cleaned
// up, so a room switch cannot start a gateway beside the old one.
func TestCallsStopDuringStartWaitsForCleanup(t *testing.T) {
	c, f := newCalls(t, true)
	first := startAsync(c, "room-a")
	<-f.started
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	<-f.cancelled // the start saw the stop and is cleaning up
	var busy *BusyError
	if _, err := c.Start("room-b"); !errors.As(err, &busy) || busy.Room != "room-a" {
		t.Fatalf("start while the old call cleans up: %v", err)
	}
	close(f.gate)
	<-stopped
	if got := <-first; got.err == nil {
		t.Fatal("the stopped start reported success")
	}
	f.hold = false
	if st, err := c.Start("room-b"); err != nil || !st.Live || st.Room != "room-b" {
		t.Fatalf("start after stop = %+v, %v", st, err)
	}
}

// Stop of a running call returns only after its gateway has exited, and
// refuses starts until then.
func TestCallsStopWaitsForTheGatewayToExit(t *testing.T) {
	c, f := newCalls(t, false)
	if _, err := c.Start("room-a"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	<-f.cancelled // the gateway is ending
	if _, err := c.Start("room-a"); err == nil || !strings.Contains(err.Error(), "still ending") {
		t.Fatalf("same room while ending: %v", err)
	}
	if _, err := c.Start("room-b"); err == nil {
		t.Fatal("another room started while the old gateway ran")
	}
	if _, _, err := c.Join(nil); err == nil {
		t.Fatal("Connect worked while the call ended")
	}
	close(f.gate)
	<-stopped
	if st, err := c.Start("room-b"); err != nil || st.Room != "room-b" {
		t.Fatalf("start after stop = %+v, %v", st, err)
	}
}

// A gateway that dies ends the call, and the status says why.
func TestCallsReportAGatewayThatExits(t *testing.T) {
	c, f := newCalls(t, false)
	close(f.gate)
	if _, err := c.Start("voice-test"); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	exited := c.live.exited
	c.mu.Unlock()
	f.exit <- errors.New("exit status 1")
	<-exited
	if st := c.Status(); st.Live || st.Room != "" || st.Error != "exit status 1" {
		t.Fatalf("status = %+v", st)
	}
}

// The app offers calls only once voice is set up, and says what is missing.
func TestCallsAreAvailableOnlyWhenVoiceIsSetUp(t *testing.T) {
	c, _ := newCalls(t, false)
	if st := c.Status(); !st.Available || st.Unavailable != "" {
		t.Fatalf("status when set up = %+v", st)
	}
	c.ready = func() string { return "Sign in to a ChatGPT account in Settings > Accounts to call Voice." }
	if st := c.Status(); st.Available || st.Live || !strings.Contains(st.Unavailable, "ChatGPT") {
		t.Fatalf("status without ChatGPT = %+v", st)
	}
}

// With no ChatGPT account in a fresh data folder, voice is unavailable.
func TestVoiceReadyWithoutChatGPT(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if why := voiceReady(); !strings.Contains(why, "ChatGPT") {
		t.Fatalf("voiceReady = %q", why)
	}
}

// fakeTimers stands in for time.AfterFunc: the test fires timers itself.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ft *fakeTimers) afterFunc(d time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.timers = append(ft.timers, t)
	return func() bool { ft.mu.Lock(); defer ft.mu.Unlock(); was := !t.stopped; t.stopped = true; return was }
}

// last is the newest timer; fire runs it even if it was stopped, as a timer
// that was already firing would.
func (ft *fakeTimers) last(t *testing.T) *fakeTimer {
	t.Helper()
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.timers) == 0 {
		t.Fatal("no abandon timer was started")
	}
	return ft.timers[len(ft.timers)-1]
}

func abandonCalls(t *testing.T) (*Calls, *fakeGateways, *fakeTimers) {
	c, f := newCalls(t, false)
	close(f.gate)
	ft := &fakeTimers{}
	c.afterFunc = ft.afterFunc
	return c, f, ft
}

// A call that no page uses for AbandonAfter ends as Hang up ends it, so a
// closed tab or a reload that never comes back leaves no gateway running.
func TestCallsEndAnAbandonedCall(t *testing.T) {
	c, _, ft := abandonCalls(t)
	if _, err := c.Start("voice-test"); err != nil {
		t.Fatal(err)
	}
	_, done, err := c.Join(nil)
	if err != nil {
		t.Fatal(err)
	}
	done() // the page reloads
	done() // done runs once
	timer := ft.last(t)
	if timer.d != AbandonAfter || AbandonAfter != 60*time.Second {
		t.Fatalf("grace = %v, AbandonAfter = %v", timer.d, AbandonAfter)
	}
	timer.f()
	if st := c.Status(); st.Live || st.Room != "" {
		t.Fatalf("the abandoned call still runs: %+v", st)
	}
}

// A page that never connects after the start abandons the call too.
func TestCallsEndACallNoPageJoined(t *testing.T) {
	c, _, ft := abandonCalls(t)
	if _, err := c.Start("voice-test"); err != nil {
		t.Fatal(err)
	}
	ft.last(t).f()
	if st := c.Status(); st.Live {
		t.Fatalf("status = %+v", st)
	}
}

// A page that reconnects within the grace period resumes the same call,
// even if the old timer was already firing.
func TestCallsKeepACallThePageRejoins(t *testing.T) {
	c, _, ft := abandonCalls(t)
	first, err := c.Start("voice-test")
	if err != nil {
		t.Fatal(err)
	}
	_, done, err := c.Join(nil)
	if err != nil {
		t.Fatal(err)
	}
	done()
	timer := ft.last(t)
	_, again, err := c.Join(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again()
	if !timer.stopped {
		t.Fatal("the reconnect left the abandon timer running")
	}
	timer.f()
	if st := c.Status(); !st.Live || st.StartedAt != first.StartedAt {
		t.Fatalf("the rejoined call ended or changed: %+v, want %+v", st, first)
	}
}

// Hang up still ends a call at once, with a page connected or not.
func TestCallsHangUpStillEndsAtOnce(t *testing.T) {
	c, _, ft := abandonCalls(t)
	if _, err := c.Start("voice-test"); err != nil {
		t.Fatal(err)
	}
	_, done, err := c.Join(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Stop()
	if st := c.Status(); st.Live {
		t.Fatalf("status = %+v", st)
	}
	done() // the page socket closes after the hang-up
	for _, timer := range ft.timers {
		timer.f() // no timer may touch a later call
	}
	if _, err := c.Start("voice-test"); err != nil {
		t.Fatal(err)
	}
	ft.timers[0].f()
	if st := c.Status(); !st.Live {
		t.Fatalf("an old call's timer ended the new call: %+v", st)
	}
}

// Hang up while the abandon timer is ending the call waits for the same
// gateway to exit, so a room switch never meets the old gateway.
func TestCallsConcurrentStopWaitsForCleanup(t *testing.T) {
	c, f := newCalls(t, false) // the gate holds gateway cleanup
	ft := &fakeTimers{}
	c.afterFunc = ft.afterFunc
	waits := make(chan struct{}, 2)
	c.waiting = func() { waits <- struct{}{} }
	if _, err := c.Start("room-a"); err != nil {
		t.Fatal(err)
	}
	go ft.last(t).f() // the abandon timer ends the call
	<-f.cancelled
	<-waits
	returned := make(chan struct{})
	go func() { c.Stop(); close(returned) }()
	select {
	case <-waits: // the hang-up waits for cleanup too
	case <-returned:
		t.Fatal("a concurrent stop returned while the gateway still ran")
	}
	if _, err := c.Start("room-b"); err == nil {
		t.Fatal("a start ran beside the old gateway")
	}
	close(f.gate)
	<-returned
	if st, err := c.Start("room-b"); err != nil || st.Room != "room-b" {
		t.Fatalf("start after both stops = %+v, %v", st, err)
	}
}

// A request can arrive after the call starts. Accepting it reuses that call.
func TestCallsAcceptRequestDuringAnExistingCall(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	c, gateways := newCalls(t, false)
	c.Open = func(string) (*RoomLink, error) { return tools.link, nil }
	speaker := newFakeSpeaker()
	originalStart := c.start
	c.start = func(ctx context.Context, link *RoomLink) (*session, error) {
		sess, err := originalStart(ctx, link)
		if err == nil {
			sess.call = requestedFakeCall{fakeCall{}, speaker}
		}
		return sess, err
	}
	first, err := c.Start(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-gateways.started
	st := tools.link.svc.CallRequestStore()
	request, _, err := st.Ask(work.ID, "CI needs your sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := c.Start(work.ID)
	if err != nil || accepted != first {
		t.Fatalf("existing call changed: %+v %v", accepted, err)
	}
	saved, err := st.Get(work.ID, request.ID)
	if err != nil || saved.State != "joined" {
		t.Fatalf("request still rings: %+v %v", saved, err)
	}
	select {
	case text := <-speaker.said:
		if !strings.Contains(text, request.Reason) {
			t.Fatalf("opening omitted reason: %s", text)
		}
	default:
		t.Fatal("request reason was not queued")
	}
	if _, send, err := st.ClaimPush(work.ID, request.ID); err != nil || send {
		t.Fatalf("joined request can push: %v %v", send, err)
	}
	if _, err := c.Start(work.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-speaker.said:
		t.Fatalf("reason repeated: %s", text)
	default:
	}
	select {
	case name := <-gateways.started:
		t.Fatalf("second gateway started: %s", name)
	default:
	}
}

type requestedFakeCall struct {
	fakeCall
	*fakeSpeaker
}
