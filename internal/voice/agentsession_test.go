package voice

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
		t.Fatalf("commit sent %s, want [audio:4 commit respond:]", got)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "turn"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "turn" {
		t.Fatalf("after commit echo = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
		t.Fatalf("commit echo sent %s, want no second reply", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(1)}
	if m := r.next(t); m.Type != agentwire.OutAudio || m.ResponseID != "r1" {
		t.Fatalf("reply audio = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(3)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync during reply = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond: audio:2 commit]" {
		t.Fatalf("busy commit sent %s, want the reply queued", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond: audio:2 commit respond:]" {
		t.Fatalf("queued reply sent %s", got)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "again"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "again" {
		t.Fatalf("after second echo = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond: audio:2 commit respond:]" {
		t.Fatalf("second echo sent %s, want no third reply", got)
	}
}

// A client commit after the provider already ended the turn must not send
// another commit or queue a second reply. Nothing new was buffered.
func TestSessionCommitAfterProviderCommitIsNoop(t *testing.T) {
	clock := newFakeClock()
	p := fakeProvider()
	p.Caps.ManualCommit = true
	p.Protocol = commitProtocol{}
	conn := newChanConn()
	r := startSession(t, fakeSessions(p, conn, clock), conn, clock, agentwire.SessionRequest{})

	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2)}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after audio = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "turn"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "turn" {
		t.Fatalf("after provider commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 respond:]" {
		t.Fatalf("provider commit sent %s, want one reply", got)
	}

	// Keepalive silence after the provider commit must not arm another reply.
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(0, 0, 0, 0)}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after silence = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError || strings.Contains(m.Message, "commit") || strings.Contains(m.Message, "buffer") {
		t.Fatalf("late commit emitted %+v, want only the sync error", m)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone}
	if m := r.next(t); m.Type != agentwire.OutResponseDone {
		t.Fatalf("response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 respond: audio:8]" {
		t.Fatalf("silence after commit sent %s, want no second commit or reply", got)
	}

	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(9)}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after later audio = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.SpeechStopped}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "end"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "end" {
		t.Fatalf("after speech_stopped = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError || strings.Contains(m.Message, "commit") || strings.Contains(m.Message, "buffer") {
		t.Fatalf("commit after speech_stopped emitted %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 respond: audio:8 audio:2]" {
		t.Fatalf("commit after speech_stopped sent %s, want no commit or reply", got)
	}

	conn.events <- realtime.Event{Kind: realtime.SpeechStarted}
	if m := r.next(t); m.Type != agentwire.OutSpeechStart {
		t.Fatalf("speech = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(0, 0)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after new speech = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 respond: audio:8 audio:2 audio:4 commit respond:]" {
		t.Fatalf("commit after speech started sent %s, want one commit and reply", got)
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
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
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
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond: respond:]" {
		t.Fatalf("next turn sent %s, want a reply after speech", got)
	}
	close(r.in)

	conn = newChanConn()
	r = startSession(t, fakeSessions(p, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.Error, Code: "server_error", Text: "provider failed"}
	if m := r.next(t); m.Type != agentwire.OutError || m.Message != "provider failed" {
		t.Fatalf("provider error = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "spoke"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "spoke" {
		t.Fatalf("after rejected commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond: respond:]" {
		t.Fatalf("after a rejected commit, sent %s, want the next turn to ask", got)
	}
}

// Manual turns turn off the provider's turn detection. Only a client commit
// ends a turn and asks for one reply. Audio and a provider speech event
// while a reply plays do not cancel it; the client sends interrupt.
// --vad-eagerness conflicts with manual, and a provider that cannot turn
// detection off rejects the option before ready.
func TestSessionManualTurns(t *testing.T) {
	clock := newFakeClock()
	plain := fakeProvider()
	closed := make(chan agentwire.InMessage)
	close(closed)
	silent := SessionIO{In: closed, Out: func(agentwire.OutMessage) error { return nil }}

	_, err := fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{Turns: "push"}, silent)
	if err == nil || !strings.Contains(err.Error(), "auto or manual") {
		t.Fatalf("bad turns error = %v", err)
	}
	_, err = fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{Turns: agentwire.TurnsManual, VADEagerness: "low"}, silent)
	if err == nil || !strings.Contains(err.Error(), "conflicts with --turns manual") {
		t.Fatalf("conflict error = %v", err)
	}
	_, err = fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{Turns: agentwire.TurnsManual}, silent)
	if err == nil || !strings.Contains(err.Error(), "does not take --turns manual") {
		t.Fatalf("unsupported turns error = %v", err)
	}

	op := realtime.OpenAI("ws://127.0.0.1/unused", func(context.Context) (string, string, error) { return "", "", nil })
	if got := openAITurn(t, op, realtime.Session{ManualTurns: true}); got != nil {
		t.Fatalf("manual turn_detection = %v, want null", got)
	}
	auto, _ := openAITurn(t, op, realtime.Session{}).(map[string]any)
	if auto["type"] != "semantic_vad" {
		t.Fatalf("auto turn_detection = %v, want semantic_vad", auto)
	}

	var gotSession realtime.Session
	capable := plain
	capable.Caps.ManualTurns = true
	capable.Caps.ManualCommit = true
	capable.Protocol = commitProtocol{}
	conn := newChanConn()
	s := fakeSessions(capable, conn, clock)
	s.dial = func(_ context.Context, _ realtime.Provider, sess realtime.Session) (sayConn, error) {
		gotSession = sess
		return conn, nil
	}
	r := startSession(t, s, conn, clock, agentwire.SessionRequest{Turns: "MANUAL"})
	if !gotSession.ManualTurns {
		t.Fatal("dialed session did not turn detection off")
	}

	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2)}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after audio = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.SpeechStarted}
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "still talking"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "still talking" {
		t.Fatalf("after provider turn events = %+v, want the transcript and no barge-in", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4]" {
		t.Fatalf("provider turn events sent %s, want no commit, cancel or reply", got)
	}

	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
		t.Fatalf("client commit sent %s, want one commit and one reply", got)
	}

	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.AudioDelta, ResponseID: "r1", Audio: pcm16(9)}
	if m := r.next(t); m.Type != agentwire.OutAudio || m.ResponseID != "r1" {
		t.Fatalf("reply audio = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(3)}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync during reply = %+v", m)
	}
	conn.events <- realtime.Event{Kind: realtime.SpeechStarted}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "barge"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "barge" {
		t.Fatalf("audio during reply = %+v, want no automatic barge-in", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond: audio:2]" {
		t.Fatalf("audio during reply sent %s, want no cancel", got)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InInterrupt}
	if m := r.next(t); m.Type != agentwire.OutSpeechStart {
		t.Fatalf("interrupt = %+v, want speech_started", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); !strings.Contains(got, "cancel") {
		t.Fatalf("interrupt sent %s, want a cancel", got)
	}

	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(4)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync on queued commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); strings.Count(got, "respond:") != 1 {
		t.Fatalf("commit during a reply sent %s, want the next reply to wait", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); strings.Count(got, "respond:") != 2 || strings.Count(got, "commit") != 2 {
		t.Fatalf("queued reply sent %s, want the waiting commit to ask once", got)
	}
}

// openAITurn is the turn_detection value of one OpenAI session setup.
func openAITurn(t *testing.T, p realtime.Provider, s realtime.Session) any {
	t.Helper()
	raw, err := json.Marshal(p.Session(s))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	audio, _ := doc["audio"].(map[string]any)
	input, _ := audio["input"].(map[string]any)
	val, ok := input["turn_detection"]
	if !ok {
		t.Fatal("turn_detection is missing; manual mode needs null, not an omitted field")
	}
	return val
}

// A client commit that the provider rejects as an empty or too-small buffer
// is silent. OpenAI uses the code input_audio_buffer_commit_empty for both
// messages, including "buffer too small ... 0.00ms". The client must not see
// an error line, and that rejection must not ask for another reply. The same
// words with a different code are still a real error.
func TestSessionLateCommitEmptyBufferIsSilent(t *testing.T) {
	clock := newFakeClock()
	p := fakeProvider()
	p.Caps.ManualCommit = true
	p.Protocol = commitProtocol{}
	conn := newChanConn()
	r := startSession(t, fakeSessions(p, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1, 2)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
		t.Fatalf("commit sent %s", got)
	}
	conn.events <- realtime.Event{
		Kind: realtime.Error,
		Code: "input_audio_buffer_commit_empty",
		Text: "Error committing input audio buffer: buffer too small. Expected at least 100ms of audio, but buffer only has 0.00ms of audio.",
	}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "later"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "later" {
		t.Fatalf("after empty-buffer rejection = %+v, want the transcript and no error line", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
		t.Fatalf("after the rejection, sent %s, want no second reply", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone}
	if m := r.next(t); m.Type != agentwire.OutResponseDone {
		t.Fatalf("response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[audio:4 commit respond:]" {
		t.Fatalf("after response done, sent %s, want no second reply", got)
	}

	conn.events <- realtime.Event{
		Kind: realtime.Error,
		Code: "server_error",
		Text: "Error committing input audio buffer: buffer too small. Expected at least 100ms of audio, but buffer only has 0.00ms of audio.",
	}
	if m := r.next(t); m.Type != agentwire.OutError || !strings.Contains(m.Message, "buffer too small") {
		t.Fatalf("unrelated error = %+v, want the provider message", m)
	}
}

// instructedProtocol names a styled response so a test can see the instructions.
type instructedProtocol struct{ sessionProtocol }

func (instructedProtocol) ResponseInstructed(key, instructions string) any {
	return "respond:" + key + ":" + instructions
}

// A session can set speaking speed and style one reply. A provider without
// the feature rejects the flag before ready, or the line with an error and
// no send. OpenAI maps speed to audio.output.speed and instructions to
// response.create.
func TestSessionSpeedAndResponseInstructions(t *testing.T) {
	clock := newFakeClock()
	plain := fakeProvider()
	closed := make(chan agentwire.InMessage)
	close(closed)
	silent := SessionIO{In: closed, Out: func(agentwire.OutMessage) error { return nil }}
	_, err := fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{Speed: 2}, silent)
	if err == nil || !strings.Contains(err.Error(), "0.25 to 1.5") {
		t.Fatalf("bad speed error = %v", err)
	}
	_, err = fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{Speed: 1.25}, silent)
	if err == nil || !strings.Contains(err.Error(), "does not take --speed") {
		t.Fatalf("unsupported speed error = %v", err)
	}

	conn := newChanConn()
	r := startSession(t, fakeSessions(plain, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InSay, Text: "hello", Instructions: "warm accent"}
	if m := r.next(t); m.Type != agentwire.OutError || !strings.Contains(m.Message, "does not take per-response instructions") {
		t.Fatalf("unsupported say instructions = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "note", Respond: true, Instructions: "warm accent"}
	if m := r.next(t); m.Type != agentwire.OutError || !strings.Contains(m.Message, "does not take per-response instructions") {
		t.Fatalf("unsupported text instructions = %+v", m)
	}
	if got := conn.sentSoFar(); len(got) != 0 {
		t.Fatalf("unsupported instructions sent %v", got)
	}
	close(r.in)

	var gotSession realtime.Session
	capable := plain
	capable.Caps.OutputSpeed = true
	capable.Caps.ResponseInstructions = true
	capable.Protocol = instructedProtocol{}
	conn = newChanConn()
	s := fakeSessions(capable, conn, clock)
	s.dial = func(_ context.Context, _ realtime.Provider, sess realtime.Session) (sayConn, error) {
		gotSession = sess
		return conn, nil
	}
	r = startSession(t, s, conn, clock, agentwire.SessionRequest{Speed: 1.25})
	if gotSession.Speed != 1.25 {
		t.Fatalf("dialed speed = %v, want 1.25", gotSession.Speed)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InSay, Text: "hello", Instructions: "warm accent"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after say = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[text:Say exactly this, word for word, and nothing else: hello respond::warm accent]" {
		t.Fatalf("say sent %s", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "the cake is ready", Respond: true, Instructions: "short and warm"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync during reply = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); !strings.Contains(got, "text:the cake is ready") || strings.Count(got, "respond:") != 1 {
		t.Fatalf("busy text sent %s, want the reply queued", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone {
		t.Fatalf("response done = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); !strings.HasSuffix(got, "respond::short and warm]") {
		t.Fatalf("queued reply sent %s, want the saved instructions", got)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InText, Text: "context only", Instructions: "do not use"}
	if m := r.next(t); m.Type != agentwire.OutError || !strings.Contains(m.Message, "respond") {
		t.Fatalf("text instructions without respond = %+v", m)
	}

	op := realtime.OpenAI("wss://example.invalid", func(context.Context) (string, string, error) { return "t", "a", nil })
	raw, err := json.Marshal(op.Session(realtime.Session{Instructions: "hi", Speed: 1.25}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"speed":1.25`) {
		t.Fatalf("OpenAI session = %s, want audio.output.speed", raw)
	}
	styled, err := json.Marshal(realtime.GA{}.ResponseInstructed("", "warm accent"))
	if err != nil {
		t.Fatal(err)
	}
	if string(styled) != `{"response":{"instructions":"warm accent"},"type":"response.create"}` {
		t.Fatalf("response.create = %s", styled)
	}
}

// replyInstructionProtocol names a styled response and a hand commit.
type replyInstructionProtocol struct{ commitProtocol }

func (replyInstructionProtocol) ResponseInstructed(key, instructions string) any {
	return "respond:" + key + ":" + instructions
}

// A session default styles every reply the session asks for, including the
// automatic reply after turn detection and the reply after a tool result.
// A commit line's own instructions replace that default for that reply.
// A provider that cannot set per-response instructions rejects the flag
// before ready, and a commit line that names instructions.
func TestSessionReplyInstructionsOnAutomaticAndToolReplies(t *testing.T) {
	clock := newFakeClock()
	plain := fakeProvider()
	closed := make(chan agentwire.InMessage)
	close(closed)
	silent := SessionIO{In: closed, Out: func(agentwire.OutMessage) error { return nil }}
	_, err := fakeSessions(plain, newChanConn(), clock).Run(t.Context(), agentwire.SessionRequest{ReplyInstructions: "steady accent"}, silent)
	if err == nil || !strings.Contains(err.Error(), "does not take --reply-instructions") {
		t.Fatalf("unsupported flag error = %v", err)
	}

	conn := newChanConn()
	r := startSession(t, fakeSessions(plain, conn, clock), conn, clock, agentwire.SessionRequest{})
	r.in <- agentwire.InMessage{Type: agentwire.InCommit, Instructions: "this turn"}
	if m := r.next(t); m.Type != agentwire.OutError || !strings.Contains(m.Message, "does not take per-response instructions") {
		t.Fatalf("unsupported commit instructions = %+v", m)
	}
	if got := conn.sentSoFar(); len(got) != 0 {
		t.Fatalf("unsupported commit instructions sent %v", got)
	}
	close(r.in)

	capable := plain
	capable.Caps.ResponseInstructions = true
	capable.Caps.ManualCommit = true
	capable.Protocol = replyInstructionProtocol{}
	conn, clock = newChanConn(), newFakeClock()
	r = startSession(t, fakeSessions(capable, conn, clock), conn, clock, agentwire.SessionRequest{
		ReplyInstructions: "steady accent",
		Tools:             []agentwire.SessionTool{{Name: "wave"}},
	})
	conn.events <- realtime.Event{Kind: realtime.Committed}
	conn.events <- realtime.Event{Kind: realtime.OwnerTranscript, Text: "heard"}
	if m := r.next(t); m.Type != agentwire.OutTranscript || m.Text != "heard" {
		t.Fatalf("after turn detection = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[respond::steady accent]" {
		t.Fatalf("automatic reply sent %s, want the session default", got)
	}

	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r1"}
	conn.events <- realtime.Event{Kind: realtime.ToolCall, CallID: "c1", Name: "wave", Args: []byte(`{}`)}
	if m := r.next(t); m.Type != agentwire.OutToolCall || m.CallID != "c1" {
		t.Fatalf("tool line = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InToolResult, CallID: "c1", Output: "done"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync during reply = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[respond::steady accent tool:c1:done]" {
		t.Fatalf("tool result while a reply is active sent %s, want the reply to wait", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r1"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r1" {
		t.Fatalf("response done = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); got != "[respond::steady accent tool:c1:done respond::steady accent]" {
		t.Fatalf("reply after tool result sent %s, want the session default", got)
	}

	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r2"}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r2"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r2" {
		t.Fatalf("released reply done = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit, Instructions: "this turn"}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); !strings.HasSuffix(got, "audio:2 commit respond::this turn]") {
		t.Fatalf("commit instructions sent %s, want them to replace the default", got)
	}
	conn.events <- realtime.Event{Kind: realtime.ResponseStarted, ResponseID: "r3"}
	conn.events <- realtime.Event{Kind: realtime.ResponseDone, ResponseID: "r3"}
	if m := r.next(t); m.Type != agentwire.OutResponseDone || m.ResponseID != "r3" {
		t.Fatalf("commit reply done = %+v", m)
	}
	r.in <- agentwire.InMessage{Type: agentwire.InAudio, PCM: pcm16(1)}
	r.in <- agentwire.InMessage{Type: agentwire.InCommit}
	r.in <- agentwire.InMessage{Type: "sync"}
	if m := r.next(t); m.Type != agentwire.OutError {
		t.Fatalf("sync after plain commit = %+v", m)
	}
	if got := fmt.Sprint(conn.sentSoFar()); !strings.HasSuffix(got, "audio:2 commit respond::steady accent]") {
		t.Fatalf("plain commit sent %s, want the session default", got)
	}
}
