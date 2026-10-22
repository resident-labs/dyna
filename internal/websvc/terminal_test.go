package websvc

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/scope"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/websvc/devicestore"
)

func TestTerminalAttachAuthAndCSRF(t *testing.T) {
	e := newEnv(t)
	cookie := e.paired()
	for _, tc := range []struct {
		name string
		q    req
		want int
	}{
		{"unpaired", req{}, 401},
		{"wrong origin", req{cookie: cookie, origin: "https://other.example"}, 403},
		{"missing origin", req{cookie: cookie, origin: "-"}, 403},
		{"missing custom header", req{cookie: cookie, noDyna: true}, 403},
		{"not JSON", req{cookie: cookie, ctype: "text/plain"}, 403},
		{"GET", req{cookie: cookie, method: "GET"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.q.url = tsApp + "/api/terminal/attach"
			tc.q.body = `{"id":"term_missing"}`
			w := e.do(e.app, tc.q)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestTerminalAttachStreamResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newEnv(t)
	s := terminalsvc.New(terminalsvc.Options{Shell: "/bin/sh"})
	defer s.Shutdown()
	e.srv.o.Terminals = s
	in, err := s.Open(roomID, "", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	cookie := e.paired()
	ts := httptest.NewServer(e.app)
	defer ts.Close()
	connect := func(after uint64) (*http.Response, context.CancelFunc) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		body, _ := json.Marshal(map[string]any{"id": in.ID, "after": after})
		req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/terminal/attach", strings.NewReader(string(body)))
		req.Host = "dyna.example.ts.net"
		req.Header.Set("Origin", tsApp)
		req.Header.Set("X-Dyna", "1")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: cookie})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			cancel()
			t.Fatalf("stream: %d", res.StatusCode)
		}
		return res, cancel
	}
	read := func(res *http.Response, needle string) terminalsvc.Event {
		scan := bufio.NewScanner(res.Body)
		scan.Buffer(make([]byte, 1<<20), 1<<20)
		for scan.Scan() {
			if !strings.HasPrefix(scan.Text(), "data: ") {
				continue
			}
			var ev terminalsvc.Event
			if json.Unmarshal([]byte(strings.TrimPrefix(scan.Text(), "data: ")), &ev) != nil {
				continue
			}
			if strings.Contains(string(ev.Data), needle) {
				return ev
			}
		}
		t.Fatalf("missing output %q: %v", needle, scan.Err())
		return terminalsvc.Event{}
	}
	res, cancel := connect(0)
	if err := s.Write(in.ID, []byte("printf '%s%s\\n' first- stream\n")); err != nil {
		t.Fatal(err)
	}
	first := read(res, "first-stream")
	cancel()
	res.Body.Close()
	// Output between connections must be replayed, without first-stream.
	if err := s.Write(in.ID, []byte("printf '%s%s\\n' second- stream\n")); err != nil {
		t.Fatal(err)
	}
	res, cancel = connect(first.Offset)
	defer cancel()
	defer res.Body.Close()
	second := read(res, "second-stream")
	if strings.Contains(string(second.Data), "first-stream") || second.Start < first.Offset {
		t.Fatalf("bad resume: %+v", second)
	}
}

// A Connect session follows the same renewal and explicit-method gates as
// ordinary calls, before the terminal manager can be reached.
type terminalExtension struct{ result AuthResult }

func (e terminalExtension) Meta(Route) map[string]any                                  { return nil }
func (e terminalExtension) ConnectSrc() []string                                       { return nil }
func (e terminalExtension) ServePublic(http.ResponseWriter, *http.Request, Route) bool { return false }
func (e terminalExtension) Authenticate(http.ResponseWriter, *http.Request, Route) (Principal, AuthResult) {
	return Principal{Kind: "connect", ID: "cs_test"}, e.result
}
func (e terminalExtension) Alive(Principal) bool { return e.result == AuthOK }
func (e terminalExtension) ServePrivate(http.ResponseWriter, *http.Request, Route, Principal) bool {
	return false
}
func (e terminalExtension) Logout(http.ResponseWriter, *http.Request, Route, Principal) {}
func TestTerminalConnectRenewalAndMethodGate(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name  string
		auth  AuthResult
		allow bool
		want  int
	}{
		{"expired access", AuthRenew, true, 401},
		{"method refused", AuthOK, false, 403},
		{"method accepted", AuthOK, true, 404}, // no manager configured in this test
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.srv.o.Ext = terminalExtension{result: tc.auth}
			grant := scope.Default()
			if tc.allow {
				grant = []string{scope.Terminal}
			}
			e.putGrant("connect", "cs_test", grant)
			w := e.do(e.app, req{url: tsApp + "/api/terminal/attach", body: `{"id":"term_missing"}`})
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
		})
	}
}
