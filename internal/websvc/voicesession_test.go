package websvc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/voice/agentwire"
)

// fakeAgentVoice answers the start line with ready, or fails when err is set.
type fakeAgentVoice struct {
	err    error
	caller chan Caller
}

func (f fakeAgentVoice) Session(_ context.Context, caller Caller, s AgentVoiceStream) error {
	f.caller <- caller
	if f.err != nil {
		return f.err
	}
	first, err := s.Read()
	if err != nil || first.Type != agentwire.InStart {
		return errors.New("no start line")
	}
	return s.Write(agentwire.OutMessage{Type: agentwire.OutReady, Rate: first.Request.Rate})
}

func TestAgentVoiceSessionRouteIsLocalOnlyAndStreamsLines(t *testing.T) {
	e := newEnv(t)
	host := fakeAgentVoice{caller: make(chan Caller, 1)}
	e.srv.o.AgentVoice = host
	e.srv.o.Callers = func(net.Conn) (Caller, error) { return Caller{Kind: Session, Session: "ses_a"}, nil }

	// The browser origin cannot reach it.
	w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/voice/session"})
	if w.Code == http.StatusSwitchingProtocols || len(host.caller) != 0 {
		t.Fatalf("the app origin reached the session route: %d", w.Code)
	}

	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: e.srv.UnixHandler(), ConnContext: e.srv.UnixConnContext}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	ws, _, err := dialer.Dial("ws://engine/api/voice/session", http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(agentwire.InMessage{Type: agentwire.InStart, Request: &agentwire.SessionRequest{Rate: 48000}}); err != nil {
		t.Fatal(err)
	}
	var m agentwire.OutMessage
	if err := ws.ReadJSON(&m); err != nil || m.Type != agentwire.OutReady || m.Rate != 48000 {
		t.Fatalf("first line = %+v, %v", m, err)
	}
	if got := <-host.caller; got.Kind != Session || got.Session != "ses_a" {
		t.Fatalf("the host got caller %+v", got)
	}
}

func TestAgentVoiceSessionRouteReportsARefusalAsAnErrorLine(t *testing.T) {
	e := newEnv(t)
	e.srv.o.AgentVoice = fakeAgentVoice{err: errors.New("the owner turned off agent voice in Settings > Voice"), caller: make(chan Caller, 1)}
	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.o.Callers = func(net.Conn) (Caller, error) { return Caller{Kind: Owner}, nil }
	server := &http.Server{Handler: e.srv.UnixHandler(), ConnContext: e.srv.UnixConnContext}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	ws, _, err := dialer.Dial("ws://engine/api/voice/session", http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	var m agentwire.OutMessage
	if err := ws.ReadJSON(&m); err != nil || m.Type != agentwire.OutError || m.Message != "the owner turned off agent voice in Settings > Voice" {
		t.Fatalf("first line = %+v, %v", m, err)
	}
	if err := ws.ReadJSON(&m); err != nil || m.Type != agentwire.OutClosed {
		t.Fatalf("second line = %+v, %v", m, err)
	}
}
