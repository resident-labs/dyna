package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeService struct {
	Service
	caller  contract.Principal
	request contract.SessionCloseRequest
	post    contract.PostRequest
}

func (f *fakeService) SessionClose(_ context.Context, c contract.Principal, r contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error) {
	f.caller, f.request = c, r
	return contract.SessionCloseResponse{}, nil
}
func (f *fakeService) Post(_ context.Context, _ contract.Principal, r contract.PostRequest) (contract.PostResponse, *contract.Error) {
	f.post = r
	return contract.PostResponse{}, nil
}
func (f *fakeService) TurnOutput(_ context.Context, c contract.Principal, _ contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error) {
	f.caller = c
	return contract.TurnOutputResponse{}, nil
}

func request(h http.Handler, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewBufferString(body)))
	return w
}
func boxCaller() Caller {
	return Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: "session-a"}, Box: "box-a", Room: "room-a"}
}

func TestBoxCallerIsListenerIdentity(t *testing.T) {
	f := &fakeService{}
	dir := t.TempDir()
	s := NewSockets(New(f, nil), dir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AddBox("box-a", "session-a", "room-a"); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "boxes", "box-a.sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	// An unknown caller field is rejected; it cannot override the listener.
	res, err := client.Post("http://unix/v1/turn.output", "application/json", bytes.NewBufferString(`{"session":"session-b"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
	res, err = client.Post("http://unix/v1/turn.output", "application/json", bytes.NewBufferString(`{"turn":"turn-b"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || f.caller.Session != "session-a" {
		t.Fatalf("status %d, caller %+v", res.StatusCode, f.caller)
	}
}

func TestHostAddRejectsBox(t *testing.T) {
	a := New(&fakeService{}, nil)
	rec := request(a.handler(boxCaller()), "/v1/host.add", `{"name":"host","ssh":"u@host","ownerOnlyAdmin":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want forbidden", rec.Code)
	}
}

func TestOwnerOnlyMethodRejectsBox(t *testing.T) {
	w := request(New(&fakeService{}, nil).handler(boxCaller()), "/v1/session.close", `{"session":"session-b"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestMalformedBodyGetsTypedError(t *testing.T) {
	w := request(New(&fakeService{}, nil).handler(boxCaller()), "/v1/turn.output", "{")
	var e contract.Error
	if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Code != contract.ErrInvalid {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
}

func TestBoxPostDefaultsToListenerRoom(t *testing.T) {
	f := &fakeService{}
	w := request(New(f, nil).handler(boxCaller()), "/v1/post", `{"text":"x"}`)
	if w.Code != http.StatusOK || f.post.Room != "room-a" {
		t.Fatalf("status %d, room %s", w.Code, f.post.Room)
	}
}

func TestBoxCannotPostInAnotherRoom(t *testing.T) {
	w := request(New(&fakeService{}, nil).handler(boxCaller()), "/v1/post", `{"room":"room-b","text":"x"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestRemovedBoxSocketFails(t *testing.T) {
	dir := t.TempDir()
	s := NewSockets(New(&fakeService{}, nil), dir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AddBox("box-a", "session-a", "room-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveBox("box-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial("unix", filepath.Join(dir, "boxes", "box-a.sock")); err == nil {
		t.Fatal("removed socket accepted a connection")
	}
	if _, err := net.Dial("unix", filepath.Join(dir, "boxes", "unknown.sock")); err == nil {
		t.Fatal("unknown socket accepted a connection")
	}
}

func TestOwnerRejectsDifferentUID(t *testing.T) {
	dir := t.TempDir()
	s := NewSockets(New(&fakeService{}, nil), dir, 1, func(net.Conn) (uint32, error) { return 2, nil })
	t.Cleanup(func() { _ = s.Close() })
	if err := s.OpenOwner(); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", filepath.Join(dir, "owner.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "POST /v1/session.close HTTP/1.1\r\nHost: local\r\nContent-Length: 23\r\n\r\n"+`{"session":"session-b"}`)
	buf := make([]byte, 128)
	if n, _ := c.Read(buf); n != 0 {
		t.Fatalf("peer received %q", buf[:n])
	}
}

type channelService struct{ Service }

func (channelService) BoxChannel(_ context.Context, c contract.Principal) (<-chan contract.BoxMessage, *contract.Error) {
	ch := make(chan contract.BoxMessage, 2)
	ch <- contract.BoxMessage{Body: contract.TurnStart{Turn: "turn-a"}}
	ch <- contract.BoxMessage{Body: contract.TurnStop{Turn: "turn-a"}}
	close(ch)
	return ch, nil
}

func TestBoxChannelResendsCurrentStateInOrder(t *testing.T) {
	h := New(channelService{}, nil).handler(boxCaller())
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/box/channel", nil))
		dec := json.NewDecoder(w.Body)
		var first, second contract.BoxMessage
		if err := dec.Decode(&first); err != nil {
			t.Fatal(err)
		}
		if err := dec.Decode(&second); err != nil {
			t.Fatal(err)
		}
		if first.Body.BoxMessageType() != "turn.start" || second.Body.BoxMessageType() != "turn.stop" {
			t.Fatalf("messages: %+v, %+v", first, second)
		}
	}
}

func TestEnginePrincipalCannotCallBoxMethod(t *testing.T) {
	c := Caller{Principal: contract.Principal{Kind: contract.PrincipalEngine, Session: "session-a"}, Box: "box-a"}
	w := request(New(&fakeService{}, nil).handler(c), "/v1/turn.output", `{}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestRestartReplacesStaleSockets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "boxes"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "owner.sock"), filepath.Join(dir, "boxes", "box-a.sock")} {
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		l.SetUnlinkOnClose(false)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s := NewSockets(New(&fakeService{}, nil), dir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.OpenOwner(); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBox("box-a", "session-a", "room-a"); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLockProtectsLiveSockets(t *testing.T) {
	dir := t.TempDir()
	first := NewSockets(New(&fakeService{}, nil), dir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = first.Close() })
	if err := first.OpenOwner(); err != nil {
		t.Fatal(err)
	}
	second := NewSockets(New(&fakeService{}, nil), dir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = second.Close() })
	if err := second.OpenOwner(); err == nil {
		t.Fatal("second engine acquired live runtime")
	}
	c, err := net.Dial("unix", filepath.Join(dir, "owner.sock"))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}
