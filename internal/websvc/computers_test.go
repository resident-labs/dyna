package websvc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc/devicestore"
)

type fakeHost struct {
	mu       sync.Mutex
	shotPath string
	fresh    []bool
	keys     []string
	opened   int
	closed   int
	server   net.Conn      // the wayvnc end of the last view
	viewed   chan struct{} // one token per opened view
	ended    chan struct{} // one token per closed view
}

func (f *fakeHost) Shot(s string, fresh bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fresh = append(f.fresh, fresh)
	return f.shotPath, nil
}
func (f *fakeHost) OpenView(s, key string) (ComputerView, error) {
	if s == "ses_gone" {
		return ComputerView{}, errors.New("no such session")
	}
	a, b := net.Pipe()
	f.mu.Lock()
	f.opened++
	f.keys = append(f.keys, key)
	f.server = b
	f.mu.Unlock()
	f.signal(f.viewed)
	return ComputerView{Conn: a, Done: func() {
		f.mu.Lock()
		f.closed++
		f.mu.Unlock()
		f.signal(f.ended)
	}}, nil
}

// signal reports one change without ever blocking the connection handler.
func (f *fakeHost) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func computerEnv(t *testing.T) (*env, *fakeHost) {
	e := newEnv(t)
	h := &fakeHost{shotPath: filepath.Join(t.TempDir(), "last.png"), viewed: make(chan struct{}, 1), ended: make(chan struct{}, 1)}
	if err := os.WriteFile(h.shotPath, []byte("\x89PNG-fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.o.Computers = h
	e.srv.shots = NewShots(h)
	return e, h
}

func TestComputerRoutesNeedAuth(t *testing.T) {
	e, _ := computerEnv(t)
	for _, p := range []string{"/api/computers/ses_a/shot.png", "/api/computers/ses_a/view"} {
		if w := e.do(e.app, req{method: "GET", url: tsApp + p}); w.Code != 401 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestComputerShotFreshIsRateLimited(t *testing.T) {
	e, h := computerEnv(t)
	c := e.paired()
	for i := 0; i < 3; i++ {
		w := e.do(e.app, req{method: "GET", url: tsApp + "/api/computers/ses_a/shot.png?fresh=1", cookie: c})
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
			t.Fatalf("shot %d: %d %v", i, w.Code, w.Header())
		}
	}
	e.do(e.app, req{method: "GET", url: tsApp + "/api/computers/ses_a/shot.png", cookie: c})
	if got := h.fresh; len(got) != 4 || !got[0] || got[1] || got[2] || got[3] {
		t.Fatalf("fresh flags %v: only the first ask in 5 s may be fresh", got)
	}
	// Another computer has its own allowance.
	e.do(e.app, req{method: "GET", url: tsApp + "/api/computers/ses_b/shot.png?fresh=1", cookie: c})
	if !h.fresh[4] {
		t.Fatal("a second computer was limited by the first")
	}
}

func TestComputerShotMissing(t *testing.T) {
	e, h := computerEnv(t)
	c := e.paired()
	_ = os.Remove(h.shotPath)
	if w := e.do(e.app, req{method: "GET", url: tsApp + "/api/computers/ses_a/shot.png", cookie: c}); w.Code != 204 {
		t.Fatalf("missing picture must be 204: %d", w.Code)
	}
}

func TestComputerConnectNeedsPermission(t *testing.T) {
	// A Connect principal is refused unless its grant includes computers.
	e, _ := computerEnv(t)
	r := httptest.NewRequest("GET", tsApp+"/api/computers/ses_a/shot.png", nil)
	rec := httptest.NewRecorder()
	e.srv.computers(rec, r, Route{App: tsApp}, "", Principal{Kind: "connect", Scopes: scope.Default()})
	if rec.Code != 403 {
		t.Fatalf("connect without permission: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	e.srv.computers(rec, r, Route{App: tsApp}, "", Principal{Kind: "connect", Scopes: []string{scope.Computers}})
	if rec.Code != 200 {
		t.Fatalf("connect with permission: %d", rec.Code)
	}
}

func TestComputerViewNeedsAKey(t *testing.T) {
	e, h := computerEnv(t)
	c := e.paired()
	for _, q := range []string{"", "?k=short", "?k=has%20space-1234"} {
		w := e.do(e.app, req{method: "GET", url: tsApp + "/api/computers/ses_a/view" + q, cookie: c, header: map[string]string{"Origin": tsApp}})
		if w.Code != 400 {
			t.Errorf("%q: %d", q, w.Code)
		}
	}
	if h.opened != 0 {
		t.Fatal("a bad key opened a viewer")
	}
}

func dialView(t *testing.T, ts *httptest.Server, cookie, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{"binary"}, HandshakeTimeout: 3 * time.Second}
	h := http.Header{}
	h.Set("Origin", origin)
	h.Set("Host", "dyna.example.ts.net")
	h.Set("Cookie", devicestore.CookieName+"="+cookie)
	return d.DialContext(context.Background(), "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/computers/ses_a/view?k=viewkey123", h)
}

func TestComputerViewRelay(t *testing.T) {
	e, h := computerEnv(t)
	cookie := e.paired()
	ts := httptest.NewServer(e.app)
	defer ts.Close()

	if _, res, err := dialView(t, ts, cookie, "https://evil.example"); err == nil || res == nil || res.StatusCode != 403 {
		t.Fatalf("a foreign origin must get 403: %v %v", err, res)
	}
	if _, res, err := dialView(t, ts, "", tsApp); err == nil || res == nil || res.StatusCode != 401 {
		t.Fatalf("no cookie must get 401: %v %v", err, res)
	}
	if h.opened != 0 {
		t.Fatalf("a refused request opened a viewer")
	}
	defer func() {
		if len(h.keys) != 1 || h.keys[0] != "viewkey123" {
			t.Errorf("the host got keys %v", h.keys)
		}
	}()

	ws, _, err := dialView(t, ts, cookie, tsApp)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Subprotocol() != "binary" {
		t.Fatalf("subprotocol %q", ws.Subprotocol())
	}
	select {
	case <-h.viewed:
	case <-time.After(2 * time.Second):
		t.Fatal("no viewer opened")
	}
	// Browser to wayvnc.
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = h.server.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := h.server.Read(buf)
	if err != nil || string(buf[:n]) != "RFB 003.008\n" {
		t.Fatalf("wayvnc got %q %v", buf[:n], err)
	}
	// wayvnc to browser.
	if _, err := h.server.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	typ, msg, err := ws.ReadMessage()
	if err != nil || typ != websocket.BinaryMessage || string(msg) != "RFB 003.008\n" {
		t.Fatalf("browser got %d %q %v", typ, msg, err)
	}
	// Closing the browser side sends view.close exactly once.
	ws.Close()
	select {
	case <-h.ended:
	case <-time.After(2 * time.Second):
		t.Fatal("no view.close")
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed != 1 {
		t.Fatalf("view.close count %d", closed)
	}
}

func TestComputerViewEndsWhenWayvncEnds(t *testing.T) {
	e, h := computerEnv(t)
	cookie := e.paired()
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	ws, _, err := dialView(t, ts, cookie, tsApp)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	select {
	case <-h.viewed:
	case <-time.After(2 * time.Second):
		t.Fatal("no viewer opened")
	}
	h.server.Close()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("the browser socket stayed open after wayvnc closed")
	}
}

func TestComputerViewRefusalIsPlainHTTP(t *testing.T) {
	e, _ := computerEnv(t)
	cookie := e.paired()
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	d := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	h := http.Header{"Origin": {tsApp}, "Host": {"dyna.example.ts.net"}, "Cookie": {devicestore.CookieName + "=" + cookie}}
	_, res, err := d.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/computers/ses_gone/view?k=viewkey123", h)
	if err == nil || res == nil || res.StatusCode != 422 {
		t.Fatalf("got %v %v", err, res)
	}
}

func TestComputerCSPUsesSameOriginForWebSocket(t *testing.T) {
	e, _ := computerEnv(t)
	w := e.do(e.app, req{method: "GET", url: tsApp + "/"})
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self';") || strings.Contains(csp, "wss://dyna.example.ts.net") {
		t.Fatalf("csp: %s", csp)
	}
}
