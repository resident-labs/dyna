package websvc

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc/devicestore"
)

const (
	tsApp   = "https://dyna.example.ts.net"
	tsCards = "https://dyna.example.ts.net:8443"
	roomID  = "room_20261017-120000-0a1b2c3d"
	attID   = "0123456789abcdef01234567"
	attID2  = "0123456789abcdef01234568"
)

type fakeAPI struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeAPI) Echo(s string, n int) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return strings.Repeat(s, n), nil
}
func (f *fakeAPI) FeedTail(room string, keep int) (string, error) {
	return room + strings.Repeat("x", keep), nil
}
func (f *fakeAPI) Fail() error       { return errors.New("wakes-nobody") }
func (f *fakeAPI) Secret() string    { return "never on the web" }
func (f *fakeAPI) Rooms() []string   { return []string{"a"} }
func (f *fakeAPI) Both() (int, bool) { return 1, true }

type env struct {
	t       *testing.T
	srv     *Server
	app     http.Handler
	cards   http.Handler
	store   *devicestore.Store
	hub     *hub.Hub
	api     *fakeAPI
	cardHit string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, err := devicestore.OpenStore(filepath.Join(t.TempDir(), "web"))
	if err != nil {
		t.Fatal(err)
	}
	// Engine startup migrates grants before any test can issue a new device.
	if err := devicegrant.Migrate(filepath.Dir(store.Dir())); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: store, hub: hub.New(1), api: &fakeAPI{}}
	calls, err := Bind([]Binding{{Name: "Echo", Access: Read, Scope: ScopeRead, Method: e.api.Echo}, {Name: "FeedTail", Access: Read, Scope: ScopeRead, Method: e.api.FeedTail}, {Name: "Fail", Access: Read, Scope: ScopeRead, Method: e.api.Fail}, {Name: "Rooms", Access: Read, Scope: ScopeRead, Method: e.api.Rooms}})
	if err != nil {
		t.Fatal(err)
	}
	routes := append(LoopbackRoutes("7457", "7458"), Route{App: tsApp, Cards: tsCards})
	e.srv, err = New(Options{
		Routes: routes,
		Store:  store,
		Shell: fstest.MapFS{
			"index.html":           {Data: []byte("<!doctype html><html><head><title>Dyna</title></head><body></body></html>")},
			"assets/app.js":        {Data: []byte("console.log(1)")},
			"manifest.webmanifest": {Data: []byte(`{"name":"Dyna"}`)},
		},
		Calls: calls,
		CallScopes: map[string]Scope{
			"Echo":     ScopeRead,
			"Fail":     ScopeRead,
			"FeedTail": ScopeRead,
			"Rooms":    ScopeRead,
		},
		Files: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("file " + r.URL.Path)) }),
		Cards: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.cardHit = r.URL.Path
			_, _ = w.Write([]byte("card"))
		}),
		Hub:       e.hub,
		Logf:      t.Logf,
		PairDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.app, e.cards = e.srv.AppHandler(), e.srv.CardHandler()
	return e
}

type req struct {
	method, url, body, origin, cookie string
	noDyna                            bool
	ctype                             string
	header                            map[string]string
}

func (e *env) do(h http.Handler, q req) *httptest.ResponseRecorder {
	e.t.Helper()
	if q.method == "" {
		q.method = http.MethodPost
	}
	r := httptest.NewRequest(q.method, q.url, strings.NewReader(q.body))
	if q.method == http.MethodPost {
		if q.origin == "" {
			q.origin = tsApp
		}
		if q.origin != "-" {
			r.Header.Set("Origin", q.origin)
		}
		if !q.noDyna {
			r.Header.Set("X-Dyna", "1")
		}
		if q.ctype == "" {
			q.ctype = "application/json"
		}
		r.Header.Set("Content-Type", q.ctype)
	}
	if q.cookie != "" {
		r.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: q.cookie})
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// paired mints a code at the "terminal" and pairs through the API.
func (e *env) paired() string {
	e.t.Helper()
	code, err := e.store.NewCode("")
	if err != nil {
		e.t.Fatal(err)
	}
	w := e.do(e.app, req{url: tsApp + "/api/pair", body: `{"code":"` + strings.ToLower(code) + `","name":"Pixel"}`})
	if w.Code != 200 {
		e.t.Fatalf("pair = %d %s", w.Code, w.Body)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Name != devicestore.CookieName || !c[0].HttpOnly || !c[0].Secure || c[0].SameSite != http.SameSiteStrictMode || c[0].MaxAge < 29*24*3600 {
		e.t.Fatalf("device cookie = %+v", c)
	}
	dev, ok := e.store.Check(c[0].Value)
	if !ok {
		e.t.Fatal("paired device missing")
	}
	// Feature tests act as a device that holds every scope. Scope tests set
	// their own grant after this.
	e.putGrant("paired", dev.ID, scope.All())
	return c[0].Value
}

func (e *env) putGrant(kind, id string, scopes []string) {
	e.t.Helper()
	grants, err := devicegrant.Open(filepath.Dir(e.store.Dir()))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := grants.Put(kind, id, scopes, false); err != nil {
		e.t.Fatal(err)
	}
}

func TestRoutesNeedADevice(t *testing.T) {
	e := newEnv(t)
	for _, q := range []req{
		{url: tsApp + "/api/Echo", body: `["a",2]`},
		{url: tsApp + "/api/Echo", body: `["a",2]`, cookie: "wrong-token"},
		{url: tsApp + "/api/CardURL", body: `["room-alpha","abc"]`},
		{url: tsApp + "/api/logout", body: `[]`},
		{method: http.MethodGet, url: tsApp + "/api/events"},
		{method: http.MethodGet, url: tsApp + "/attachments/room-alpha/abc"},
	} {
		if w := e.do(e.app, q); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "pair this device") {
			t.Errorf("%s %s cookie=%q = %d %s", q.method, q.url, q.cookie, w.Code, w.Body)
		}
	}
	if e.api.calls != 0 {
		t.Fatal("an unpaired request reached the API")
	}
}

func TestPairing(t *testing.T) {
	e := newEnv(t)
	// No code lives: the same plain error, and nothing is counted.
	w := e.do(e.app, req{url: tsApp + "/api/pair", body: `{"code":"AAAAA-AAAAA"}`})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), devicestore.ErrPair.Error()) {
		t.Fatalf("no code = %d %s", w.Code, w.Body)
	}
	code, _ := e.store.NewCode("")
	raw, _ := os.ReadFile(filepath.Join(e.store.Dir(), "pair.json"))
	if strings.Contains(string(raw), devicestore.NormalizeCode(code)) || strings.Contains(string(raw), code) {
		t.Fatal("pair.json must not hold the code")
	}
	if fi, _ := os.Stat(filepath.Join(e.store.Dir(), "pair.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("pair.json mode = %v", fi.Mode())
	}
	// CSRF on the pair route itself.
	for _, q := range []req{
		{url: tsApp + "/api/pair", body: `{"code":"` + code + `"}`, origin: "-"},
		{url: tsApp + "/api/pair", body: `{"code":"` + code + `"}`, origin: "https://evil.example"},
		{url: tsApp + "/api/pair", body: `{"code":"` + code + `"}`, noDyna: true},
		{url: tsApp + "/api/pair", body: `{"code":"` + code + `"}`, ctype: "text/plain"},
	} {
		if w := e.do(e.app, q); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Fatalf("pair CSRF %+v = %d", q, w.Code)
		}
	}
	// Four wrong tries leave the code alive; the fifth kills it.
	for i := 0; i < devicestore.CodeTries; i++ {
		if w := e.do(e.app, req{url: tsApp + "/api/pair", body: `{"code":"ZZZZZ-ZZZZZ"}`}); w.Code != http.StatusForbidden {
			t.Fatalf("wrong try %d = %d", i, w.Code)
		}
	}
	if w := e.do(e.app, req{url: tsApp + "/api/pair", body: `{"code":"` + code + `"}`}); w.Code != http.StatusForbidden {
		t.Fatalf("a code must die after %d wrong tries: %d", devicestore.CodeTries, w.Code)
	}
	// A fresh code pairs once, then is spent.
	tok := e.paired()
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["ab",2]`, cookie: tok}); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `"abab"` {
		t.Fatalf("paired call = %d %s", w.Code, w.Body)
	}
	devs, _ := e.store.Devices()
	if len(devs) != 1 || devs[0].Name != "Pixel" || strings.Contains(devs[0].Hash, tok) {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestPairGrantFollowsTheRoute(t *testing.T) {
	e := newEnv(t)
	grants, err := devicegrant.Open(filepath.Dir(e.store.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	pairAt := func(app, code string) devicegrant.Grant {
		t.Helper()
		w := e.do(e.app, req{url: app + "/api/pair", body: `{"code":"` + strings.ToLower(code) + `","name":"Browser"}`, origin: app})
		if w.Code != 200 {
			t.Fatalf("pair at %s = %d %s", app, w.Code, w.Body)
		}
		c := w.Result().Cookies()
		if len(c) != 1 {
			t.Fatalf("cookies at %s = %+v", app, c)
		}
		dev, ok := e.store.Check(c[0].Value)
		if !ok {
			t.Fatal("paired device missing")
		}
		g, ok, err := grants.Get("paired", dev.ID)
		if err != nil || !ok {
			t.Fatalf("grant = %+v ok=%v err=%v", g, ok, err)
		}
		return g
	}
	// This computer's browser is the owner's own: every scope, trusted like a shell.
	code, _ := e.store.NewCode("")
	if g := pairAt("http://127.0.0.1:7457", code); g.Legacy || !reflect.DeepEqual(g.Scopes, scope.All()) {
		t.Fatalf("loopback grant = %+v", g)
	}
	code, _ = e.store.NewCode("")
	if g := pairAt("http://localhost:7457", code); g.Legacy || !reflect.DeepEqual(g.Scopes, scope.All()) {
		t.Fatalf("localhost grant = %+v", g)
	}
	// A device paired from anywhere else keeps the default grant.
	code, _ = e.store.NewCode("")
	if g := pairAt(tsApp, code); g.Legacy || !reflect.DeepEqual(g.Scopes, scope.Default()) {
		t.Fatalf("remote grant = %+v", g)
	}
	// A code the owner narrowed on purpose keeps its scopes everywhere.
	code, _ = e.store.NewScopedCode("", []string{scope.Read})
	if g := pairAt("http://127.0.0.1:7457", code); g.Legacy || !reflect.DeepEqual(g.Scopes, []string{scope.Read}) {
		t.Fatalf("narrowed grant = %+v", g)
	}
}

func TestNewCodeKillsTheOldOneAndCodesExpire(t *testing.T) {
	e := newEnv(t)
	old, _ := e.store.NewCode("")
	if _, err := e.store.NewCode(""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.Pair(old, "x"); !errors.Is(err, devicestore.ErrPair) {
		t.Fatalf("an old code must die: %v", err)
	}
	code, _ := e.store.NewCode("")
	e.store.SetClock(func() time.Time { return time.Now().Add(devicestore.CodeLife + time.Second) })
	if _, _, err := e.store.Pair(code, "x"); !errors.Is(err, devicestore.ErrPair) {
		t.Fatalf("an expired code must fail: %v", err)
	}
}

func TestLoopbackCookieIsNotSecure(t *testing.T) {
	e := newEnv(t)
	code, _ := e.store.NewCode("")
	w := e.do(e.app, req{url: "http://127.0.0.1:7457/api/pair", origin: "http://127.0.0.1:7457", body: `{"code":"` + code + `"}`,
		header: map[string]string{"X-Forwarded-Proto": "https"}})
	if w.Code != 200 || w.Result().Cookies()[0].Secure {
		t.Fatalf("loopback pair = %d %+v", w.Code, w.Result().Cookies())
	}
}

func TestCSRFAndHost(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	for name, q := range map[string]req{
		"no origin":      {url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, origin: "-"},
		"other origin":   {url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, origin: "https://evil.example"},
		"other route":    {url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, origin: "http://127.0.0.1:7457"},
		"no X-Dyna":      {url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, noDyna: true},
		"form body":      {url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, ctype: "application/x-www-form-urlencoded"},
		"GET a method":   {method: http.MethodGet, url: tsApp + "/api/Echo", cookie: tok},
		"logout no csrf": {url: tsApp + "/api/logout", body: `[]`, cookie: tok, noDyna: true},
	} {
		if w := e.do(e.app, q); w.Code != http.StatusForbidden {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	// Unknown hosts (DNS rebinding, a proxy that rewrites Host) get 421.
	w := e.do(e.app, req{method: http.MethodGet, url: "http://rebind.example/", header: map[string]string{"X-Forwarded-Host": "dyna.example.ts.net"}})
	if w.Code != http.StatusMisdirectedRequest || !strings.Contains(w.Body.String(), "rebind.example") {
		t.Fatalf("unknown host = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.cards, req{method: http.MethodGet, url: tsApp + "/rich/room-alpha/abc"}); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("the app host on the card listener = %d", w.Code)
	}
	// Funnel traffic is refused before anything else.
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, header: map[string]string{"Tailscale-Funnel-Request": "?1"}}); w.Code != http.StatusForbidden {
		t.Fatalf("funnel = %d", w.Code)
	}
	if e.api.calls != 0 {
		t.Fatal("a refused request reached the API")
	}
	// Identity headers grant nothing.
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, header: map[string]string{"Tailscale-User-Login": "owner@example.com"}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("identity header = %d", w.Code)
	}
}

func TestAPICalls(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	if w := e.do(e.app, req{url: tsApp + "/api/Fail", body: `[]`, cookie: tok}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"wakes-nobody"`) {
		t.Fatalf("error = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Secret", body: `[]`, cookie: tok}); w.Code != http.StatusNotFound {
		t.Fatalf("an unbound method = %d", w.Code)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a"]`, cookie: tok}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong arity = %d", w.Code)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `{"a":1}`, cookie: tok}); w.Code != http.StatusBadRequest {
		t.Fatalf("not an array = %d", w.Code)
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/attachments/room-alpha/abc", cookie: tok}); w.Code != 200 || w.Body.String() != "file /attachments/room-alpha/abc" {
		t.Fatalf("attachment = %d %s", w.Code, w.Body)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/logout", body: `[]`, cookie: tok}); w.Code != 200 {
		t.Fatalf("logout = %d", w.Code)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok}); w.Code != http.StatusUnauthorized {
		t.Fatalf("after logout = %d", w.Code)
	}
}

func TestFeedTailRepliesAnswerNotModifiedForTheSameReply(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	first := e.do(e.app, req{url: tsApp + "/api/FeedTail", body: `["a",3]`, cookie: tok})
	tag := first.Header().Get("ETag")
	if first.Code != 200 || tag == "" || first.Body.Len() == 0 {
		t.Fatalf("first reply = %d, ETag %q, %d body bytes", first.Code, tag, first.Body.Len())
	}
	again := e.do(e.app, req{url: tsApp + "/api/FeedTail", body: `["a",3]`, cookie: tok, header: map[string]string{"If-None-Match": tag}})
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != tag {
		t.Fatalf("same reply = %d, ETag %q, %d body bytes", again.Code, again.Header().Get("ETag"), again.Body.Len())
	}
	changed := e.do(e.app, req{url: tsApp + "/api/FeedTail", body: `["a",4]`, cookie: tok, header: map[string]string{"If-None-Match": tag}})
	if changed.Code != 200 || changed.Header().Get("ETag") == tag {
		t.Fatalf("changed reply = %d, ETag %q", changed.Code, changed.Header().Get("ETag"))
	}
	other := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok})
	if other.Header().Get("ETag") != "" {
		t.Fatalf("only FeedTail sends an ETag, got %q", other.Header().Get("ETag"))
	}
}

func TestShellIsPublicAndLockedDown(t *testing.T) {
	e := newEnv(t)
	w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/"})
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `<meta name="dyna-web" content="{&#34;cardOrigin&#34;:&#34;`+tsCards+`&#34;,&#34;push&#34;:false}">`) {
		t.Fatalf("index = %d %s", w.Code, body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self' 'wasm-unsafe-eval';", "connect-src 'self'", "frame-src " + tsCards + ";", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("shell CSP lacks %q: %s", want, csp)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("index headers = %v", w.Header())
	}
	// The loopback route names its own card origin.
	w = e.do(e.app, req{method: http.MethodGet, url: "http://localhost:7457/"})
	if !strings.Contains(w.Body.String(), "http://localhost:7458") {
		t.Fatalf("loopback index = %s", w.Body)
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/manifest.webmanifest"}); w.Code != 200 || w.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest = %d %v", w.Code, w.Header())
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/assets/app.js"}); w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset = %d %v", w.Code, w.Header())
	}
	for _, p := range []string{"/rich/room-alpha/abc", "/nothing", "/../index.html"} {
		if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + p}); w.Code != http.StatusNotFound && p != "/../index.html" {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
}

func TestCardTickets(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	w := e.do(e.app, req{url: tsApp + "/api/CardURL", body: `["` + roomID + `","` + attID + `"]`, cookie: tok})
	var ref CardRef
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ref) != nil || !strings.HasPrefix(ref.URL, tsCards+"/rich/"+roomID+"/"+attID+"?t=") {
		t.Fatalf("CardURL = %d %s", w.Code, w.Body)
	}
	if d := time.Until(ref.Expires); d < TicketLife-time.Minute || d > TicketLife {
		t.Fatalf("ticket life = %v", d)
	}
	if w := e.do(e.cards, req{method: http.MethodGet, url: ref.URL}); w.Code != 200 || e.cardHit != "/rich/"+roomID+"/"+attID {
		t.Fatalf("card = %d %q", w.Code, e.cardHit)
	}
	// The card origin needs no cookie, and a cookie alone grants nothing.
	t1 := ref.URL[strings.Index(ref.URL, "?t=")+3:]
	for name, u := range map[string]string{
		"no ticket":   tsCards + "/rich/" + roomID + "/" + attID,
		"cookie only": tsCards + "/rich/" + roomID + "/" + attID,
		"other card":  tsCards + "/rich/room-alpha/abc124?t=" + t1,
		"other room":  tsCards + "/rich/room-beta/abc123?t=" + t1,
		"bent mac":    tsCards + "/rich/room-alpha/abc123?t=" + t1[:len(t1)-2] + "xx",
		"not rich":    tsCards + "/attachments/room-alpha/abc123?t=" + t1,
		"api":         tsCards + "/api/Echo?t=" + t1,
		"index":       tsCards + "/",
	} {
		q := req{method: http.MethodGet, url: u}
		if name == "cookie only" {
			q.cookie = tok
		}
		if w := e.do(e.cards, q); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d", name, w.Code)
		}
	}
	// An expired ticket fails.
	e.srv.tickets.now = func() time.Time { return time.Now().Add(TicketLife + time.Second) }
	if w := e.do(e.cards, req{method: http.MethodGet, url: ref.URL}); w.Code != http.StatusNotFound {
		t.Fatalf("expired ticket = %d", w.Code)
	}
	if FrameAncestors(e.srv.o.Routes) != "http://127.0.0.1:7457 http://localhost:7457 "+tsApp {
		t.Fatalf("frame-ancestors = %q", FrameAncestors(e.srv.o.Routes))
	}
}

func TestParseRoute(t *testing.T) {
	r, err := ParseRoute("HTTPS://Dyna.Example.ts.net:443/,https://dyna.example.ts.net:8443")
	if err != nil || r.App != "https://dyna.example.ts.net" || r.Cards != tsCards {
		t.Fatalf("route = %+v %v", r, err)
	}
	if r, err := ParseRoute("http://[::1]:80,http://[::1]:7458"); err != nil || r.App != "http://[::1]" || r.Cards != "http://[::1]:7458" {
		t.Fatalf("IPv6 route = %+v %v", r, err)
	}
	for _, bad := range []string{"https://a", "https://a,https://a", "ftp://a,https://b", "https://a/x,https://b", "https://u@a,https://b"} {
		if _, err := ParseRoute(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestEventsStreamAndRevoke(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	srv := httptest.NewServer(e.app)
	defer srv.Close()
	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	r.Host = "dyna.example.ts.net"
	r.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: tok})
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events = %d %v", resp.StatusCode, resp.Header)
	}
	for e.hub.Stats().Streams == 0 {
		time.Sleep(time.Millisecond)
	}
	e.hub.Publish(hub.Event{Topic: "pins", Room: roomID})
	sc := bufio.NewScanner(resp.Body)
	got := ""
	sawHello := false
	for sc.Scan() {
		if sc.Text() == "event: hello" {
			sawHello = true
		}
		if strings.HasPrefix(sc.Text(), "data: ") && sc.Text() != "data: {}" {
			got = sc.Text()
			break
		}
	}
	if !sawHello {
		t.Fatal("a stream must start with a hello event")
	}
	if got != `data: {"kind":"pins","roomId":"`+roomID+`"}` {
		t.Fatalf("event = %q", got)
	}
	// A second process revokes the device: the next request fails at once.
	other, _ := devicestore.OpenStore(e.store.Dir())
	if n, err := other.Revoke("all"); n != 1 || err != nil {
		t.Fatalf("revoke = %d %v", n, err)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok}); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked device = %d", w.Code)
	}
}

func TestLongPoll(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	get := func(q string) PollReply {
		t.Helper()
		w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/events/poll" + q, cookie: tok})
		var r PollReply
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil {
			t.Fatalf("poll%s = %d %s", q, w.Code, w.Body)
		}
		return r
	}
	if w := e.do(e.app, req{method: http.MethodGet, url: tsApp + "/api/events/poll"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired poll = %d", w.Code)
	}
	start := get("")
	at := func(seq uint64) string {
		return "?after=" + strconv.FormatInt(start.Epoch, 10) + ":" + strconv.FormatUint(seq, 10)
	}
	e.hub.Publish(hub.Event{Topic: "rooms"})
	e.hub.Publish(hub.Event{Topic: "pins", Room: roomID})
	r := get(at(start.Seq))
	if r.Seq != start.Seq+2 || len(r.Events) != 2 || string(r.Events[1]) != `{"kind":"pins","roomId":"`+roomID+`"}` || r.Reset {
		t.Fatalf("poll = %+v", r)
	}
	// A poll returns the next event even when it races with publication.
	done := make(chan PollReply, 1)
	go func() { done <- get(at(r.Seq)) }()
	e.hub.Publish(hub.Event{Topic: "members"})
	select {
	case got := <-done:
		if len(got.Events) != 1 {
			t.Fatalf("woken poll = %+v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a waiting poll did not wake")
	}
	// A client too far behind is told to reload.
	for range hub.Keep + 5 {
		e.hub.Publish(hub.Event{Topic: "posts"})
	}
	if r := get(at(start.Seq)); !r.Reset {
		t.Fatalf("stale poll = %+v", r)
	}
	// A client of an earlier engine start reloads once.
	if r := get("?after=999:1"); !r.Reset || r.Epoch != start.Epoch {
		t.Fatalf("old-epoch poll = %+v", r)
	}
	if r := get(at(99999999)); !r.Reset {
		t.Fatalf("future poll = %+v", r)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Pixel 9":                  "Pixel 9",
		"evil\x1b]0;pwned\x07name": "evil]0;pwnedname",
		"":                         "browser",
		"  a \n\t b  ":             "a b",
		strings.Repeat("é", 30):    strings.Repeat("é", 20),
		"rtl‮override":             "rtloverride",
	} {
		if got := devicestore.CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLastSeenIsThrottled(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	p := filepath.Join(e.store.Dir(), "devices.json")
	before, _ := os.Stat(p)
	for i := 0; i < 20; i++ {
		e.store.Check(tok)
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("Check rewrote devices.json inside a minute")
	}
	e.store.SetClock(func() time.Time { return time.Now().Add(2 * time.Minute) })
	e.store.Check(tok)
	devs, _ := e.store.Devices()
	if time.Until(devs[0].LastSeen) < time.Minute {
		t.Fatalf("last seen = %v", devs[0].LastSeen)
	}
}

func TestParallelCalls(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok}); w.Code != 200 {
				t.Errorf("parallel = %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if e.api.calls != 32 {
		t.Fatalf("calls = %d", e.api.calls)
	}
}

func TestLargeRepliesAreGzipped(t *testing.T) {
	e := newEnv(t)
	tok := e.paired()
	big := `["abc ",5000]`
	plain := e.do(e.app, req{url: tsApp + "/api/Echo", body: big, cookie: tok})
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("no Accept-Encoding: %d, encoding %q", plain.Code, plain.Header().Get("Content-Encoding"))
	}
	w := e.do(e.app, req{url: tsApp + "/api/Echo", body: big, cookie: tok, header: map[string]string{"Accept-Encoding": "gzip, br"}})
	if w.Header().Get("Content-Encoding") != "gzip" || w.Body.Len() >= plain.Body.Len()/4 {
		t.Fatalf("gzip reply: encoding %q, %d bytes against %d", w.Header().Get("Content-Encoding"), w.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != plain.Body.String() {
		t.Fatalf("the gzip body differs from the plain body")
	}
	// A small reply and a refused encoding stay plain.
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok, header: map[string]string{"Accept-Encoding": "gzip"}}); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("a small reply must not be compressed")
	}
	if w := e.do(e.app, req{url: tsApp + "/api/Echo", body: big, cookie: tok, header: map[string]string{"Accept-Encoding": "gzip;q=0"}}); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip;q=0 must not be compressed")
	}
}

// Desktop launches replace one device while phone codes still add devices.
func TestPairWithFixedIDReplacesDevice(t *testing.T) {
	e := newEnv(t)
	phoneToken := e.paired()
	pairDesktop := func() string {
		t.Helper()
		code, err := e.store.NewCode("desktop")
		if err != nil {
			t.Fatal(err)
		}
		token, dev, err := e.store.Pair(code, "Dyna desktop")
		if err != nil || dev.ID != "desktop" {
			t.Fatalf("desktop pairing = %+v, %v", dev, err)
		}
		return token
	}
	old := pairDesktop()
	current := pairDesktop()
	if _, ok := e.store.Check(old); ok {
		t.Fatal("old desktop token still works")
	}
	if _, ok := e.store.Check(current); !ok {
		t.Fatal("new desktop token does not work")
	}
	if _, ok := e.store.Check(phoneToken); !ok {
		t.Fatal("desktop pairing revoked the phone")
	}
	devices, err := e.store.Devices()
	if err != nil || len(devices) != 2 {
		t.Fatalf("want one desktop and one phone, got %+v, %v", devices, err)
	}
}

func TestUnixAuthStaysOnUnixHandler(t *testing.T) {
	e := newEnv(t)
	q := req{url: "http://engine/api/Echo", body: `["socket",1]`, origin: "-", noDyna: true}
	w := e.do(asCaller(e.srv.UnixHandler(), owner), q)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `"socket"` {
		t.Fatalf("Unix call = %d %s", w.Code, w.Body)
	}
	q.url = tsApp + "/api/Echo"
	q.header = map[string]string{"X-Dyna-Local": "1", "X-Dyna-Principal": "local"}
	w = e.do(e.app, q)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("browser bypassed pairing = %d %s", w.Code, w.Body)
	}
	q.cookie = e.paired()
	w = e.do(e.app, q)
	if w.Code != http.StatusForbidden {
		t.Fatalf("browser bypassed CSRF = %d %s", w.Code, w.Body)
	}
	q.url = "http://engine/api/Echo"
	q.method = http.MethodGet
	w = e.do(asCaller(e.srv.UnixHandler(), owner), q)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Unix GET = %d %s", w.Code, w.Body)
	}
	q.method = http.MethodPost
	q.ctype = "text/plain"
	w = e.do(asCaller(e.srv.UnixHandler(), owner), q)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Unix non-JSON = %d %s", w.Code, w.Body)
	}
}

func TestPairRecordsCodeScopes(t *testing.T) {
	e := newEnv(t)
	code, err := e.store.NewScopedCode("", []string{"terminal", "read"})
	if err != nil {
		t.Fatal(err)
	}
	w := e.do(e.app, req{url: tsApp + "/api/pair", body: `{"code":"` + code + `","name":"phone"}`})
	if w.Code != http.StatusOK {
		t.Fatalf("pair = %d %s", w.Code, w.Body)
	}
	var body struct{ Device string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	grants, err := devicegrant.Open(filepath.Dir(e.store.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := grants.Get("paired", body.Device)
	if err != nil || !ok || got.Legacy || !reflect.DeepEqual(got.Scopes, []string{scope.Read, scope.Terminal}) {
		t.Fatalf("grant = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestADeviceInUseGetsAFreshCookie(t *testing.T) {
	for _, heartbeat := range []bool{false, true} {
		name := "API request"
		if heartbeat {
			name = "stream heartbeat before API request"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			tok := e.paired()
			dev, ok := e.store.Check(tok)
			if !ok {
				t.Fatal("paired device missing")
			}
			now := dev.Created.Add(3 * 24 * time.Hour)
			e.store.SetClock(func() time.Time { return now })
			if heartbeat && !e.srv.alive(tok, Principal{Kind: "device", ID: dev.ID}) {
				t.Fatal("the stream lost a valid device")
			}
			w := e.do(e.app, req{url: tsApp + "/api/Echo", body: `["a",1]`, cookie: tok})
			if w.Code != 200 {
				t.Fatalf("echo = %d %s", w.Code, w.Body)
			}
			c := w.Result().Cookies()
			if len(c) != 1 || c[0].Name != devicestore.CookieName || c[0].MaxAge < int((devicestore.DeviceLife-time.Hour)/time.Second) {
				t.Fatalf("no fresh device cookie = %+v", c)
			}
			if d, ok := e.store.Check(tok); !ok || !d.Expires.After(now.Add(devicestore.DeviceLife-devicestore.RenewEvery)) {
				t.Fatalf("expiry did not slide = %+v ok=%v", d, ok)
			}
		})
	}
}
