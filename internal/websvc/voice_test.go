package websvc

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/voice/realtime"
	"dyna-agent/internal/websvc/devicestore"
)

// fakeVoice is a call service. When live, it hands each joined page to
// pages and each page message to got, and closes left when the page leaves.
type fakeVoice struct {
	live  bool
	pages chan *realtime.Page
	got   chan realtime.PageMessage
	left  chan struct{}
}

func (f fakeVoice) Join(p *realtime.Page) (func(realtime.PageMessage), func(), error) {
	if !f.live {
		return nil, nil, errors.New("no voice call is live")
	}
	f.pages <- p
	return func(m realtime.PageMessage) { f.got <- m }, func() { close(f.left) }, nil
}

func dialVoice(ts *httptest.Server, cookie, origin string) (*websocket.Conn, *http.Response, error) {
	return dialVoiceQuery(ts, cookie, origin, "")
}

func dialVoiceQuery(ts *httptest.Server, cookie, origin, query string) (*websocket.Conn, *http.Response, error) {
	d := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	h := http.Header{"Origin": {origin}, "Host": {"dyna.example.ts.net"}}
	if cookie != "" {
		h.Set("Cookie", devicestore.CookieName+"="+cookie)
	}
	return d.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/voice/realtime"+query, h)
}

func TestVoiceRealtimeRefusals(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Voice = fakeVoice{}
	cookie := e.paired()
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	for _, c := range []struct {
		name, cookie, origin string
		code                 int
	}{
		{"no cookie", "", tsApp, 401},
		{"foreign origin", cookie, "https://evil.example", 403},
		{"no live call", cookie, tsApp, 409},
	} {
		if _, res, err := dialVoice(ts, c.cookie, c.origin); err == nil || res == nil || res.StatusCode != c.code {
			t.Errorf("%s: want %d, got %v %v", c.name, c.code, err, res)
		}
	}
}

func TestVoiceRealtimeProtocolVersion(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Voice = fakeVoice{}
	cookie := e.paired()
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	for _, c := range []struct {
		query string
		code  int
	}{{"?v=3", 400}, {"?v=0", 400}, {"?v=x", 400}} {
		if _, res, err := dialVoiceQuery(ts, cookie, tsApp, c.query); err == nil || res == nil || res.StatusCode != c.code {
			t.Errorf("%s: want %d, got %v %v", c.query, c.code, err, res)
		}
	}
	for _, q := range []string{"", "?v=1", "?v=2"} { // no version means the web page's version 1
		f := fakeVoice{live: true, pages: make(chan *realtime.Page, 1), got: make(chan realtime.PageMessage, 1), left: make(chan struct{})}
		e.srv.o.Voice = f
		ws, _, err := dialVoiceQuery(ts, cookie, tsApp, q)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		<-f.pages
		ws.Close()
		<-f.left
	}
}

func TestVoiceRealtimeJoinsThePageToTheCall(t *testing.T) {
	e := newEnv(t)
	f := fakeVoice{live: true, pages: make(chan *realtime.Page, 1), got: make(chan realtime.PageMessage, 1), left: make(chan struct{})}
	e.srv.o.Voice = f
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	ws, _, err := dialVoice(ts, e.paired(), tsApp)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	page := <-f.pages
	if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"mute","muted":true}`)); err != nil {
		t.Fatal(err)
	}
	if m := <-f.got; m.Type != "mute" || !m.Muted {
		t.Fatalf("the call got %+v", m)
	}
	page.Send(realtime.PageEvent{Type: "audio", ID: "i1", Audio: []byte{1, 2}})
	page.Close(realtime.CloseTakenOver, "taken over")
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, msg, err := ws.ReadMessage(); err != nil || string(msg) != `{"type":"audio","id":"i1","audio":"AQI="}` {
		t.Fatalf("page got %q %v", msg, err)
	}
	// The call's close code reaches the page, so it can say the call is
	// open in another window.
	_, _, err = ws.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != realtime.CloseTakenOver || ce.Text != "taken over" {
		t.Fatalf("close = %v", err)
	}
	<-f.left // the page left the call
}

// A control message marks the end of this input batch. If binary audio is
// dropped, the next legacy message exposes the loss without a timeout.
func TestVoiceRealtimeDeliversBinaryAndLegacyAudio(t *testing.T) {
	e := newEnv(t)
	f := fakeVoice{live: true, pages: make(chan *realtime.Page, 1), got: make(chan realtime.PageMessage, 3), left: make(chan struct{})}
	e.srv.o.Voice = f
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	ws, _, err := dialVoice(ts, e.paired(), tsApp)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	<-f.pages
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 255, 127}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"audio\",\"audio\":\"AIAAAA==\"}")); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"mute\",\"muted\":true}")); err != nil {
		t.Fatal(err)
	}
	if m := <-f.got; m.Type != "audio" || string(m.Audio) != string([]byte{0, 0, 255, 127}) {
		t.Fatalf("binary microphone audio was lost or changed: %+v", m)
	}
	if m := <-f.got; m.Type != "audio" || string(m.Audio) != string([]byte{0, 128, 0, 0}) {
		t.Fatalf("legacy JSON microphone audio was lost or changed: %+v", m)
	}
	if m := <-f.got; m.Type != "mute" || !m.Muted {
		t.Fatalf("control message changed after audio: %+v", m)
	}
}
