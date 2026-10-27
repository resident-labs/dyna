package voice

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/voice/realtime"
)

// sayProtocol labels the client events so a test can see what was sent.
type sayProtocol struct{ realtime.GA }

func (sayProtocol) UserText(text string) any      { return "text:" + text }
func (sayProtocol) ResponseCreate(key string) any { return "respond:" + key }

// fakeSayConn replays events and records what the say sends.
type fakeSayConn struct {
	sent   []any
	events []realtime.Event
	closed bool
}

func (c *fakeSayConn) Send(e any) error { c.sent = append(c.sent, e); return nil }
func (c *fakeSayConn) Close() error     { c.closed = true; return nil }
func (c *fakeSayConn) Next() (realtime.Event, error) {
	e := c.events[0]
	c.events = c.events[1:]
	return e, nil
}

func fakeSayer(conn *fakeSayConn, p realtime.Provider, session *realtime.Session) *Sayer {
	p.Protocol = sayProtocol{}
	return &Sayer{
		provider: func(string) (realtime.Provider, error) { return p, nil },
		dial: func(_ context.Context, _ realtime.Provider, s realtime.Session) (sayConn, error) {
			*session = s
			return conn, nil
		},
		timeout: time.Second,
		slots:   make(chan struct{}, MaxSays),
	}
}

func TestSayJoinsAudioInOrderAndStopsAtResponseDone(t *testing.T) {
	conn := &fakeSayConn{events: []realtime.Event{
		{Kind: realtime.ResponseStarted},
		{Kind: realtime.AudioDelta, Audio: []byte{1, 2}},
		{Kind: realtime.AudioDelta, Audio: []byte{3, 4}},
		{Kind: realtime.ResponseDone},
		{Kind: realtime.AudioDelta, Audio: []byte{9, 9}}, // after done: never read
	}}
	var session realtime.Session
	s := fakeSayer(conn, realtime.Provider{Label: "Fake", OutputRate: 24000}, &session)
	res, err := s.Say(context.Background(), SayRequest{Text: " Hello there ", Voice: "marin", Style: "a village helper"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Audio, []byte{1, 2, 3, 4}) || res.SampleRate != 24000 {
		t.Errorf("result = %v at %d Hz, want the deltas in order at 24000", res.Audio, res.SampleRate)
	}
	if len(conn.sent) != 2 || conn.sent[0] != "text:Hello there" || conn.sent[1] != "respond:" {
		t.Errorf("sent = %v, want one text item and one response request", conn.sent)
	}
	if len(session.Tools) != 0 || session.Voice != "marin" || !strings.Contains(session.Instructions, "a village helper") {
		t.Errorf("session = %+v, want no tools, voice marin and the style", session)
	}
	if !conn.closed || len(conn.events) != 1 {
		t.Errorf("closed = %v, events left = %d, want closed with the late delta unread", conn.closed, len(conn.events))
	}
}

func TestSayRefusesProviderWithoutClientResponses(t *testing.T) {
	var session realtime.Session
	s := fakeSayer(&fakeSayConn{}, realtime.Provider{Label: "Fake", Caps: realtime.Capabilities{NoClientResponses: true}}, &session)
	if _, err := s.Say(context.Background(), SayRequest{Text: "hi"}); err == nil || !strings.Contains(err.Error(), "cannot speak text") {
		t.Errorf("err = %v, want a clear provider error", err)
	}
}

func TestSayRefusesAThirdAtOnce(t *testing.T) {
	var session realtime.Session
	s := fakeSayer(&fakeSayConn{}, realtime.Provider{Label: "Fake"}, &session)
	for range MaxSays {
		s.slots <- struct{}{}
	}
	if _, err := s.Say(context.Background(), SayRequest{Text: "hi"}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("err = %v, want a busy error", err)
	}
}
