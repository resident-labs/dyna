package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/voice/agentwire"
	"dyna-agent/internal/voice/realtime"
)

// sessionProtocol labels the client events so a test can see what was sent.
type sessionProtocol struct{ sayProtocol }

func (sessionProtocol) AudioAppend(pcm []byte) any        { return fmt.Sprintf("audio:%d", len(pcm)) }
func (sessionProtocol) ResponseCancel() any               { return "cancel" }
func (sessionProtocol) FunctionOutput(id, out string) any { return "tool:" + id + ":" + out }
func (sessionProtocol) UserImage(mime string, data []byte) any {
	return fmt.Sprintf("image:%s:%d", mime, len(data))
}

// chanConn is a provider connection that a test feeds with events.
type chanConn struct {
	events chan realtime.Event
	mu     sync.Mutex
	sent   []any
	closed bool
}

func newChanConn() *chanConn { return &chanConn{events: make(chan realtime.Event, 16)} }

func (c *chanConn) Send(e any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, e)
	return nil
}

func (c *chanConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *chanConn) Next() (realtime.Event, error) {
	ev, ok := <-c.events
	if !ok {
		return realtime.Event{}, io.EOF
	}
	return ev, nil
}

func (c *chanConn) sentSoFar() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.sent...)
}

// fakeClock hands out timers that fire only when a test says so.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[time.Duration][]chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_000_000, 0), timers: map[time.Duration][]chan time.Time{}}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers[d] = append(c.timers[d], ch)
	return ch
}

// fire moves the clock by d and fires every timer set for exactly d.
func (c *fakeClock) fire(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, ch := range c.timers[d] {
		ch <- c.now
	}
	delete(c.timers, d)
}

// sessionRun is one running session and the channels around it.
type sessionRun struct {
	in      chan agentwire.InMessage
	out     chan agentwire.OutMessage
	conn    *chanConn
	clock   *fakeClock
	started chan string
	result  chan runResult
}

type runResult struct {
	end SessionEnd
	err error
}

func fakeProvider() realtime.Provider {
	return realtime.Provider{
		Key: "fake", Label: "Fake Voice", InputRate: 24000, OutputRate: 24000,
		Caps:     realtime.Capabilities{FunctionCalling: true, ImageInput: true},
		Protocol: sessionProtocol{},
	}
}

func fakeSessions(p realtime.Provider, conn *chanConn, clock *fakeClock) *Sessions {
	return &Sessions{
		provider: func(string) (realtime.Provider, error) { return p, nil },
		dial:     func(context.Context, realtime.Provider, realtime.Session) (sayConn, error) { return conn, nil },
		now:      clock.Now,
		after:    clock.After,
		slot:     make(chan struct{}, 1),
	}
}

// startSession runs a session and returns once it has said ready.
func startSession(t *testing.T, s *Sessions, conn *chanConn, clock *fakeClock, req agentwire.SessionRequest) *sessionRun {
	t.Helper()
	r := &sessionRun{in: make(chan agentwire.InMessage), out: make(chan agentwire.OutMessage, 64), conn: conn, clock: clock, started: make(chan string, 1), result: make(chan runResult, 1)}
	go func() {
		end, err := s.Run(t.Context(), req, SessionIO{
			In:      r.in,
			Out:     func(m agentwire.OutMessage) error { r.out <- m; return nil },
			Started: func(p string) { r.started <- p },
		})
		r.result <- runResult{end, err}
	}()
	select {
	case m := <-r.out:
		if m.Type != agentwire.OutReady {
			t.Fatalf("first line = %+v, want ready", m)
		}
	case res := <-r.result:
		t.Fatalf("the session ended before ready: %+v", res)
	}
	return r
}

// next reads the next line to the agent.
func (r *sessionRun) next(t *testing.T) agentwire.OutMessage {
	t.Helper()
	select {
	case m := <-r.out:
		return m
	case <-t.Context().Done():
		t.Fatal("no line came")
		return agentwire.OutMessage{}
	}
}

func pcm16(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func TestSessionMapsProviderEventsToLines(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{Rate: 48000})
	if p := <-r.started; p != "fake" {
		t.Fatalf("started provider = %q", p)
	}
	for _, ev := range []realtime.Event{
		{Kind: realtime.ResponseStarted, ResponseID: "r1"},
		{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(10, 20)},
		{Kind: realtime.OwnerTranscript, Text: "hello"},
		{Kind: realtime.VoiceTranscript, Text: "hi there"},
		{Kind: realtime.ResponseDone, ResponseID: "r1", Text: "completed"},
	} {
		conn.events <- ev
	}
	audio := r.next(t)
	if audio.Type != agentwire.OutAudio || audio.ResponseID != "r1" || len(audio.PCM) != 8 {
		t.Fatalf("audio line = %+v; want 4 samples at 48 kHz from 2 at 24 kHz", audio)
	}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Role != agentwire.RoleUser || m.Text != "hello" {
		t.Fatalf("user transcript = %+v", m)
	}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Role != agentwire.RoleAssistant || m.Text != "hi there" {
		t.Fatalf("assistant transcript = %+v", m)
	}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("done line = %+v", m)
	}
}

func TestSessionAudioInIsResampledAndAnswered(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{Rate: 48000})
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2, 3, 4)}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "x"}
	r.next(t) // the transcript line comes after the reply request
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 respond:]" {
		t.Fatalf("sent = %s; want 2 samples at 24 kHz, then a reply request", got)
	}
}

func TestSessionBargeInCancelsAndTellsTheAgent(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{})
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.SpeechStarted}
	if m := r.next(t); m.Type != agentwire.OutSpeechStart {
		t.Fatalf("line = %+v, want speech_started", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[cancel]" {
		t.Fatalf("sent = %s", got)
	}
}

func TestSessionToolCallRoundTripAndTimeout(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{
		Tools: []agentwire.SessionTool{{Name: "wave", Description: "wave", Parameters: []byte(`{"type":"object"}`)}},
	})
	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c1", Name: "wave", Args: []byte(`{"n":1}`)}
	call := r.next(t)
	if call.Type != agentwire.OutToolCall || call.CallID != "c1" || call.Name != "wave" || string(call.Args) != `{"n":1}` {
		t.Fatalf("tool line = %+v", call)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c1", Output: "done"}
	// A second call is never answered: the 10 second timer gives it an error.
	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c2", Name: "wave"}
	r.next(t)
	clock.fire(realtime.ToolTimeout)
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("the timeout gave %+v, want an error line", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c2", Output: "late"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("a late result gave %+v, want an error line", m)
	}
	// The first reply still holds the slot, so the timeout's reply waits for it.
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "x"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone {
		t.Fatalf("got %+v, want response_done", m)
	}
	r.next(t)
	want := `[tool:c1:done respond: tool:c2:{"error":"the tool did not answer in time"} respond:]`
	if got := fmt.Sprint(conn.sentSoFar()); got != want {
		t.Fatalf("sent = %s\nwant %s", got, want)
	}
}

func TestSessionEndsOnEOFAndClosesProvider(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{})
	clock.fire(5 * time.Second) // time passes before the end
	close(r.in)
	res := <-r.result
	if res.err != nil || res.end.Reason != agentwire.EndInputClosed || res.end.Provider != "fake" || res.end.Elapsed != 5*time.Second {
		t.Fatalf("result = %+v", res)
	}
	if m := r.next(t); m.Type != agentwire.OutClosed || m.Reason != agentwire.EndInputClosed {
		t.Fatalf("last line = %+v", m)
	}
	if !conn.closed {
		t.Fatal("the provider connection stayed open")
	}
}

func TestSessionLimitsEndIt(t *testing.T) {
	for name, tc := range map[string]struct {
		req    agentwire.SessionRequest
		fire   time.Duration
		reason agentwire.EndReason
	}{
		"time limit": {agentwire.SessionRequest{MaxMinutes: 2}, 2 * time.Minute, agentwire.EndTimeLimit},
		"no audio":   {agentwire.SessionRequest{}, agentwire.SessionIdle, agentwire.EndIdle},
	} {
		t.Run(name, func(t *testing.T) {
			conn, clock := newChanConn(), newFakeClock()
			r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, tc.req)
			clock.fire(tc.fire)
			if res := <-r.result; res.err != nil || res.end.Reason != tc.reason {
				t.Fatalf("result = %+v, want %s", res, tc.reason)
			}
		})
	}
}

func TestSecondSessionIsBusy(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	s := fakeSessions(fakeProvider(), conn, clock)
	r := startSession(t, s, conn, clock, agentwire.SessionRequest{})
	_, err := s.Run(t.Context(), agentwire.SessionRequest{}, SessionIO{})
	if err == nil || err.Error() != "voice is busy: an agent voice session is already running; try again when it ends" {
		t.Fatalf("second session error = %v", err)
	}
	close(r.in)
	<-r.result
	// The slot is free again once the first ends.
	r2 := startSession(t, s, conn, clock, agentwire.SessionRequest{})
	close(r2.in)
	<-r2.result
}

func TestSessionRefusesWhatTheOwnerOrProviderCannotDo(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	off := fakeSessions(fakeProvider(), conn, clock)
	off.enabled = func() error { return ErrAgentVoiceOff }
	if _, err := off.Run(t.Context(), agentwire.SessionRequest{}, SessionIO{}); !errors.Is(err, ErrAgentVoiceOff) {
		t.Fatalf("with agent voice off: %v", err)
	}
	noTools := fakeProvider()
	noTools.Caps.FunctionCalling = false
	_, err := fakeSessions(noTools, conn, clock).Run(t.Context(), agentwire.SessionRequest{Tools: []agentwire.SessionTool{{Name: "wave"}}}, SessionIO{})
	if err == nil {
		t.Fatal("a provider without function calling took --tools")
	}
}

func TestSessionTextSayAndImage(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "a player joined"}
	r.in <- agentwire.InMessage{Type: agentwire.InSay, Text: "Welcome"}
	r.in <- agentwire.InMessage{Type: agentwire.InImage, Mime: "image/png", Image: []byte{1, 2, 3}}
	r.in <- agentwire.InMessage{Type: agentwire.InImage, Mime: "image/gif", Image: []byte{1}}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("a gif gave %+v, want an error line", m)
	}
	want := "[text:a player joined text:Say exactly this, word for word, and nothing else: Welcome respond: image:image/png:3]"
	if got := fmt.Sprint(conn.sentSoFar()); got != want {
		t.Fatalf("sent = %s\nwant %s", got, want)
	}
}

func TestSessionRefusesImageAndTextTheProviderCannotTake(t *testing.T) {
	p := fakeProvider()
	p.Caps = realtime.Capabilities{NoClientResponses: true}
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(p, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InImage, Mime: "image/png", Image: []byte{1}}
	r.in <- agentwire.InMessage{Type: agentwire.InSay, Text: "hi"}
	for _, what := range []string{"image", "say"} {
		if m := r.next(t); m.Type != agentwire.OutError {
			t.Fatalf("%s gave %+v, want an error line", what, m)
		}
	}
	if sent := conn.sentSoFar(); len(sent) != 0 {
		t.Fatalf("sent = %v; the provider takes neither", sent)
	}
}

func TestSessionSayReplaceCancelsAndAppendWaits(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{})
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(1, 2)}
	if m := r.next(t); m.Type != agentwire.OutAudio {
		t.Fatalf("got %+v, want the audio of r1", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InSay, Text: "one", Mode: agentwire.SayAppend}
	r.in <- agentwire.InMessage{Type: agentwire.InSay, Text: "two", Mode: agentwire.SayReplace}
	if m := r.next(t); m.Type != agentwire.OutSpeechStart {
		t.Fatalf("replace gave %+v, want speech_started", m)
	}
	conn.events <- realtime.Event{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(1, 2)}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone {
		t.Fatalf("got %+v, want response_done with no audio of the cancelled reply", m)
	}
	want := "[text:Say exactly this, word for word, and nothing else: one text:Say exactly this, word for word, and nothing else: two cancel respond:]"
	if got := fmt.Sprint(conn.sentSoFar()); got != want {
		t.Fatalf("sent = %s\nwant %s", got, want)
	}
}

func TestSessionTextRespondAsksOnceAndWaitsLikeAppend(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "a player joined"}
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "what changed?", Respond: true}
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(1, 2)}
	if m := r.next(t); m.Type != agentwire.OutAudio || m.ResponseID != "r1" {
		t.Fatalf("got %+v, want audio of the reply that plays", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "[BACKEND] the door is open", Respond: true}
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: " "}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("an empty text line gave %+v, want an error line", m)
	}
	held := "[text:a player joined text:what changed? respond: text:[BACKEND] the door is open]"
	if got := fmt.Sprint(conn.sentSoFar()); got != held {
		t.Fatalf("while speech plays, sent = %s\nwant %s", got, held)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("got %+v, want response_done for r1", m)
	}
	want := "[text:a player joined text:what changed? respond: text:[BACKEND] the door is open respond:]"
	if got := fmt.Sprint(conn.sentSoFar()); got != want {
		t.Fatalf("sent = %s\nwant %s", got, want)
	}
}

// A tool result that arrives before response.done must not send a second
// response.create. The fake provider has no SingleResponseSlot flag, so this
// is the OpenAI path: the response that made the call is still active.
func TestSessionToolResultWaitsWhileAResponseIsActive(t *testing.T) {
	conn, clock := newChanConn(), newFakeClock()
	r := startSession(t, fakeSessions(fakeProvider(), conn, clock), conn, clock, agentwire.SessionRequest{
		Tools: []agentwire.SessionTool{{Name: "wave"}},
	})
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c1", Name: "wave", Args: []byte(`{}`)}
	if m := r.next(t); m.Type != agentwire.OutToolCall || m.CallID != "c1" {
		t.Fatalf("tool line = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c1", Output: "done"}
	// The unknown line is handled only after the tool result, so the send has finished.
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync line gave %+v, want an error line", m)
	}
	held := "[tool:c1:done]"
	if got := fmt.Sprint(conn.sentSoFar()); got != held {
		t.Fatalf("while r1 is active, sent = %s\nwant %s", got, held)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "x"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("got %+v, want response_done", m)
	}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "x" {
		t.Fatalf("got %+v, want the transcript after the queued reply", m)
	}
	want := "[tool:c1:done respond:]"
	if got := fmt.Sprint(conn.sentSoFar()); got != want {
		t.Fatalf("sent = %s\nwant %s", got, want)
	}
}

// quietProtocol records a later or silent result without asking for speech.
type quietProtocol struct{ sessionProtocol }

func (quietProtocol) QuietFunctionOutput(id, out string) any { return "quiet:" + id + ":" + out }

// A later or silent tool keeps its result as context and asks for no reply,
// including after the response that called it ends. A plain tool still asks.
func TestSessionLaterAndSilentToolsDoNotAskForAReply(t *testing.T) {
	p := fakeProvider()
	p.Protocol = quietProtocol{}
	closed := make(chan agentwire.InMessage)
	close(closed)
	_, err := fakeSessions(p, newChanConn(), newFakeClock()).Run(t.Context(), agentwire.SessionRequest{
		Tools: []agentwire.SessionTool{{Name: "x", After: "wait"}},
	}, SessionIO{In: closed, Out: func(agentwire.OutMessage) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), `"wait"`) {
		t.Fatalf("bad after = %v, want a refusal that names the value", err)
	}

	conn, clock := newChanConn(), newFakeClock()
	var seen []realtime.Tool
	s := fakeSessions(p, conn, clock)
	s.dial = func(_ context.Context, _ realtime.Provider, sess realtime.Session) (sayConn, error) {
		seen = append([]realtime.Tool(nil), sess.Tools...)
		return conn, nil
	}
	r := startSession(t, s, conn, clock, agentwire.SessionRequest{
		Tools: []agentwire.SessionTool{
			{Name: "wave"},
			{Name: "dispatch", After: agentwire.ToolAfterLater},
			{Name: "hush", After: agentwire.ToolAfterSilent},
		},
	})
	wantMark := []struct {
		name          string
		later, silent bool
	}{
		{"wave", false, false},
		{"dispatch", true, false},
		{"hush", false, true},
	}
	if len(seen) != len(wantMark) {
		t.Fatalf("provider tools = %+v", seen)
	}
	for i, want := range wantMark {
		got := seen[i]
		if got.Name != want.name || got.Later != want.later || got.Silent != want.silent {
			t.Fatalf("tool %d = %+v, want %s later=%v silent=%v", i, got, want.name, want.later, want.silent)
		}
	}

	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c1", Name: "dispatch"}
	if m := r.next(t); m.Type != agentwire.OutToolCall || m.Name != "dispatch" {
		t.Fatalf("tool line = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c1", Output: "started"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync gave %+v, want an error line", m)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("got %+v, want response_done with no queued reply", m)
	}

	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c2", Name: "hush"}
	if m := r.next(t); m.Type != agentwire.OutToolCall || m.CallID != "c2" {
		t.Fatalf("tool line = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c2", Output: "ok"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync gave %+v, want an error line", m)
	}

	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c3", Name: "wave"}
	if m := r.next(t); m.Type != agentwire.OutToolCall || m.CallID != "c3" {
		t.Fatalf("tool line = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c3", Output: "done"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync gave %+v, want an error line", m)
	}
	want := "[quiet:c1:started quiet:c2: tool:c3:done respond:]"
	if got := fmt.Sprint(conn.sentSoFar()); got != want {
		t.Fatalf("sent = %s\nwant %s", got, want)
	}
}

// commitProtocol names a manual turn commit so a test can see it.
type commitProtocol struct{ sessionProtocol }

func (commitProtocol) AudioCommit() any { return "commit" }

// A client can set turn-detection eagerness and end a turn by hand. A provider
// without the feature rejects the flag before ready, or the commit line with
// an error and no send. A commit asks for one reply through the same queue as
// a text line with respond, and the provider's commit echo does not ask again.
func TestSessionCommitAndVADEagerness(t *testing.T) {
	clock := newFakeClock()
	plain := fakeProvider()
	closed := make(chan agentwire.InMessage)
	close(closed)
	_, err := fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{VADEagerness: "fast"}, SessionIO{
		In: closed, Out: func(agentwire.OutMessage) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "low, medium, high or auto") {
		t.Fatalf("bad eagerness error = %v", err)
	}
	_, err = fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{VADEagerness: "high"}, SessionIO{
		In: closed, Out: func(agentwire.OutMessage) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "does not take --vad-eagerness") {
		t.Fatalf("unsupported flag error = %v", err)
	}

	conn := newChanConn()
	r := startSession(t, fakeSessions(plain, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	if m := r.next(t); m.Type != agentwire.OutError || !strings.Contains(m.Message, "cannot commit a turn") {
		t.Fatalf("unsupported commit = %+v", m)
	}
	if got := conn.sentSoFar(); len(got) != 0 {
		t.Fatalf("unsupported commit sent %v", got)
	}
	close(r.in)

	var gotSession realtime.Session
	capable := plain
	capable.Caps.ManualCommit = true
	capable.Caps.VADEagerness = true
	capable.Protocol = commitProtocol{}
	conn = newChanConn()
	s := fakeSessions(capable, conn, clock)
	s.dial = func(_ context.Context, _ realtime.Provider, sess realtime.Session) (sayConn, error) {
		gotSession = sess
		return conn, nil
	}
	r = startSession(t, s, conn, clock, agentwire.SessionRequest{VADEagerness: "HIGH"})
	if gotSession.VADEagerness != "high" {
		t.Fatalf("dialed eagerness = %q, want high", gotSession.VADEagerness)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after idle commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond:]" {
		t.Fatalf("idle commit sent %s, want [commit respond:]", got)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "turn"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "turn" {
		t.Fatalf("after commit echo = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond:]" {
		t.Fatalf("commit echo sent %s, want no second reply", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(1)}
	if m := r.next(t); m.Type != agentwire.OutAudio || m.ResponseID != "r1" {
		t.Fatalf("reply audio = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync during reply = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond: commit]" {
		t.Fatalf("busy commit sent %s, want the reply queued", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond: commit respond:]" {
		t.Fatalf("queued reply sent %s", got)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "again"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "again" {
		t.Fatalf("after second echo = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond: commit respond:]" {
		t.Fatalf("second echo sent %s, want no third reply", got)
	}
}

// A commit that gets no echo must not swallow the next real turn. OpenAI
// sends no committed event when the buffer was already committed or was
// empty. Speech starting, or a provider error, ends that pending echo.
func TestSessionCommitWithoutEchoDoesNotSwallowTheNextTurn(t *testing.T) {
	clock := newFakeClock()
	p := fakeProvider()
	p.Caps.ManualCommit = true
	p.Protocol = commitProtocol{}
	conn := newChanConn()
	r := startSession(t, fakeSessions(p, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond:]" {
		t.Fatalf("commit sent %s", got)
	}
	conn.events <- realtime.Event{Kind: realtime.SpeechStarted}
	if m := r.next(t); m.Type != agentwire.OutSpeechStart {
		t.Fatalf("speech = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone}
	if m := r.next(t); m.Type != agentwire.OutResponseDone {
		t.Fatalf("done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond: respond:]" {
		t.Fatalf("next turn sent %s, want a reply after speech", got)
	}
	close(r.in)

	conn = newChanConn()
	r = startSession(t, fakeSessions(p, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.Error, Code: "input_audio_buffer_commit_empty", Text: "buffer is empty"}
	if m := r.next(t); m.Type != agentwire.OutError || m.Message != "buffer is empty" {
		t.Fatalf("provider error = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "spoke"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "spoke" {
		t.Fatalf("after rejected commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[commit respond: respond:]" {
		t.Fatalf("after a rejected commit, sent %s, want the next turn to ask", got)
	}
}
