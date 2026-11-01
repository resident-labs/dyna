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

func (f *fakeService) OpSubmit(context.Context, contract.Principal, contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error) {
	return contract.OpSubmitResponse{}, nil
}

func (f *fakeService) RoomList(context.Context, contract.Principal, contract.RoomListRequest) (contract.RoomListResponse, *contract.Error) {
	return contract.RoomListResponse{}, nil
}
func (f *fakeService) SessionList(context.Context, contract.Principal, contract.SessionListRequest) (contract.SessionListResponse, *contract.Error) {
	return contract.SessionListResponse{}, nil
}

type fakeService struct {
	Service
	caller  contract.Principal
	request contract.SessionCloseRequest
	restart contract.SessionRestartRequest
	post    contract.PostRequest
}

func (f *fakeService) SessionStart(_ context.Context, c contract.Principal, r contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error) {
	f.caller = c
	return contract.SessionStartResponse{}, nil
}

func (f *fakeService) SessionClose(_ context.Context, c contract.Principal, r contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error) {
	f.caller, f.request = c, r
	return contract.SessionCloseResponse{}, nil
}
func (f *fakeService) SessionRestart(_ context.Context, c contract.Principal, r contract.SessionRestartRequest) (contract.SessionRestartResponse, *contract.Error) {
	f.caller, f.restart = c, r
	return contract.SessionRestartResponse{}, nil
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

type helloService struct{ *fakeService }

func (helloService) Events(context.Context, contract.Principal, contract.RoomID, int64) (<-chan contract.Event, *contract.Error) {
	ch := make(chan contract.Event)
	close(ch)
	return ch, nil
}

func TestEventsStartWithBuildHello(t *testing.T) {
	a := New(helloService{&fakeService{}}, nil)
	a.Build = "b"
	w := httptest.NewRecorder()
	a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}).ServeHTTP(w, httptest.NewRequest("GET", "/v1/events", nil))
	if got, want := w.Body.String(), "event: hello\ndata: {\"build\":\"b\"}\n\n"; got != want {
		t.Fatalf("stream starts with %q, want %q", got, want)
	}
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

func TestSessionRestartPassesCaller(t *testing.T) {
	f := &fakeService{}
	a := New(f, nil)
	w := request(a.handler(boxCaller()), "/v1/session.restart", `{"session":"session-a"}`)
	if w.Code != http.StatusOK || f.caller.Session != "session-a" {
		t.Fatalf("box status = %d, caller %+v", w.Code, f.caller)
	}
	w = request(a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}), "/v1/session.restart", `{"session":"session-a"}`)
	if w.Code != http.StatusOK || f.caller.Kind != contract.PrincipalOwner || f.restart.Session != "session-a" {
		t.Fatalf("owner status=%d caller=%+v request=%+v", w.Code, f.caller, f.restart)
	}
}

func TestHostAddRejectsBox(t *testing.T) {
	a := New(&fakeService{}, nil)
	rec := request(a.handler(boxCaller()), "/v1/host.add", `{"name":"host","ssh":"u@host","ownerOnlyAdmin":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want forbidden", rec.Code)
	}
}

func TestGitRouteUsesBoxListenerIdentity(t *testing.T) {
	a := New(&fakeService{}, nil)
	var caller Caller
	a.SetGitRoute(func(c Caller) http.Handler {
		caller = c
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	w := request(a.handler(boxCaller()), "/git/owner/repo.git/git-upload-pack", "")
	if w.Code != http.StatusNoContent || caller.Room != "room-a" || caller.Principal.Session != "session-a" {
		t.Fatalf("status %d, caller %+v", w.Code, caller)
	}
}

func TestOwnerOnlyMethodRejectsBox(t *testing.T) {
	w := request(New(&fakeService{}, nil).handler(boxCaller()), "/v1/room.repo.add", `{"room":"room-a","repo":"a/b"}`)
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

func (channelService) BoxChannel(_ context.Context, c contract.Principal, _ string) (<-chan contract.BoxMessage, *contract.Error) {
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

type streamService struct {
	Service
	after int64
}

func (s *streamService) Events(_ context.Context, _ contract.Principal, _ contract.RoomID, after int64) (<-chan contract.Event, *contract.Error) {
	s.after = after
	ch := make(chan contract.Event)
	close(ch)
	return ch, nil
}

func TestEventsReconnectUsesLastEventID(t *testing.T) {
	s := &streamService{}
	h := New(s, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}})
	r := httptest.NewRequest("GET", "/v1/events?room=room-a&after=2", nil)
	r.Header.Set("Last-Event-ID", "9")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || s.after != 9 {
		t.Fatalf("status %d after %d", w.Code, s.after)
	}
}

func TestOpSubmitRejectsHostexecRun(t *testing.T) {
	body := `{"operation":{"kind":"hostexec.run","room":"room-a","args":{"room":"room-a","host":"host","argv":["true"],"timeout":1000,"grant":"grant"}}}`
	response := request(New(&fakeService{}, nil).handler(boxCaller()), "/v1/op.submit", body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
}

func TestShellWatchRejectsBox(t *testing.T) {
	a := New(&fakeService{}, nil)
	w := httptest.NewRecorder()
	a.handler(boxCaller()).ServeHTTP(w, httptest.NewRequest("GET", "/v1/shell/watch?session=session-a", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

func TestOwnerStreamRejectsSession(t *testing.T) {
	h := New(&fakeService{}, nil).handler(boxCaller())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/events?after=0", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestBoxCanStartChild(t *testing.T) {
	f := &fakeService{}
	w := request(New(f, nil).handler(boxCaller()), "/v1/session.start", `{"room":"room-a","name":"child","profile":"test"}`)
	if w.Code != http.StatusOK || f.caller.Session != "session-a" {
		t.Fatalf("status=%d caller=%+v", w.Code, f.caller)
	}
}

func TestOpSubmitRejectsUnknownOperationFields(t *testing.T) {
	a := New(&fakeService{}, nil)
	for _, body := range []string{
		`{"operation":{"kind":"pr.open","args":{"title":"x","unknown":true}}}`,
		`{"operation":{"kind":"hostexec.grant","args":{"hosts":["h"],"reason":"x","unknown":true}}}`,
		`{"operation":{"kind":"pr.open","unknown":true,"args":{"title":"x"}}}`,
	} {
		got := request(a.handler(boxCaller()), "/v1/op.submit", body)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("unknown field accepted: %s: %d %s", body, got.Code, got.Body)
		}
	}
}
