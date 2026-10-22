package connect

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

const (
	tunnelApp = "https://m-abc.residentlabs.dev"
	tsApp     = "https://desk.example.ts.net"
	ownerID   = "usr_owner"
	devID     = "dev_phone"
)

// fakeRelay answers grant checks and key set fetches.
type fakeRelay struct {
	mu     sync.Mutex
	key    ed25519.PrivateKey
	active bool
	epoch  int
	down   bool
	gone   bool
	status int // when set, grant checks answer this HTTP status
	grants int
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	switch r.URL.Path {
	case "/.well-known/jwks.json":
		json.NewEncoder(w).Encode(map[string]any{"keys": []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "k1", X: b64.EncodeToString(f.key.Public().(ed25519.PublicKey))}}})
	case "/v1/machine/grant":
		f.grants++
		if f.status != 0 {
			w.WriteHeader(f.status)
			json.NewEncoder(w).Encode(map[string]string{"error": "busy"})
			return
		}
		if f.gone {
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{"error": "unlinked"})
			return
		}
		json.NewEncoder(w).Encode(Grant{Active: f.active, Epoch: f.epoch})
	default:
		http.NotFound(w, r)
	}
}

type harness struct {
	t       *testing.T
	relay   *fakeRelay
	relaySv *httptest.Server
	store   *Store
	svc     *Service
	web     http.Handler
	now     time.Time
	mu      sync.Mutex
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	_, rk, _ := ed25519.GenerateKey(rand.Reader)
	h := &harness{t: t, relay: &fakeRelay{key: rk, active: true, epoch: 1}, now: time.Now()}
	h.relaySv = httptest.NewServer(h.relay)
	t.Cleanup(h.relaySv.Close)
	root := t.TempDir()
	st, err := OpenStore(filepath.Join(root, "connect"))
	if err != nil {
		t.Fatal(err)
	}
	st.now = h.clock
	h.store = st
	if err := st.SaveLink(Link{Relay: h.relaySv.URL, Machine: "abc", Name: "desk", Owner: Owner{ID: ownerID, Name: "Sam"}, App: tunnelApp, Cards: "https://mc-abc.residentlabs.dev", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveKeySet(KeySet{Keys: []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "k1", X: b64.EncodeToString(rk.Public().(ed25519.PublicKey))}}, Fetched: h.now}); err != nil {
		t.Fatal(err)
	}
	routes := []Route{
		{Kind: "loopback", Route: websvc.Route{App: "http://127.0.0.1:7457", Cards: "http://127.0.0.1:7458"}},
		{Kind: "tailscale", Route: websvc.Route{App: tsApp, Cards: tsApp + ":8443"}},
		{Kind: "tunnel", Route: websvc.Route{App: tunnelApp, Cards: "https://mc-abc.residentlabs.dev"}},
	}
	svc, err := New(Options{Store: st, Routes: routes, Now: h.clock})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	wst, err := devicestore.OpenStore(filepath.Join(root, "web"))
	if err != nil {
		t.Fatal(err)
	}
	var wr []websvc.Route
	for _, r := range routes {
		wr = append(wr, r.Route)
	}
	srv, err := websvc.New(websvc.Options{
		Routes: wr, Store: wst, Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}},
		Calls: map[string]websvc.Call{
			"Health": func(context.Context, []json.RawMessage) (any, error) { return "ok", nil },
			"Secret": func(context.Context, []json.RawMessage) (any, error) { return "secret", nil },
		},
		CallScopes: map[string]websvc.Scope{"Health": websvc.ScopeRead},
		Files:      http.NotFoundHandler(), Cards: http.NotFoundHandler(), Hub: hub.New(1), Ext: svc,
		Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.web = srv.AppHandler()
	return h
}

// relayJWS signs a relay request to the machine.
func (h *harness) relayJWS(claims map[string]any) string {
	hd, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "k1", "typ": "dyna-connect+jws"})
	now := h.clock().Unix()
	base := map[string]any{"iss": h.relaySv.URL, "aud": "abc", "iat": now, "exp": now + 60, "nonce": randomToken(16), "epoch": 1}
	for k, v := range claims {
		base[k] = v
	}
	p, _ := json.Marshal(base)
	in := b64.EncodeToString(hd) + "." + b64.EncodeToString(p)
	return in + "." + b64.EncodeToString(ed25519.Sign(h.relay.key, []byte(in)))
}

type browser struct {
	key    *ecdsa.PrivateKey
	origin string
	cookie *http.Cookie
}

func newBrowser(origin string) *browser {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &browser{key: k, origin: origin}
}

func (b *browser) jwk() ecJWK {
	pub, _ := b.key.PublicKey.Bytes()
	return ecJWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(pub[1:33]), Y: b64.EncodeToString(pub[33:])}
}

func (b *browser) jkt() string { return b.jwk().thumbprint() }

func (b *browser) proof(path string, iat time.Time) string {
	jwk, _ := json.Marshal(b.jwk())
	hd, _ := json.Marshal(map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": json.RawMessage(jwk)})
	p, _ := json.Marshal(map[string]any{"htm": "POST", "htu": b.origin + path, "iat": iat.Unix(), "jti": randomToken(16)})
	in := b64.EncodeToString(hd) + "." + b64.EncodeToString(p)
	d := sha256.Sum256([]byte(in))
	r, s, _ := ecdsa.Sign(rand.Reader, b.key, d[:])
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + b64.EncodeToString(sig)
}

func (h *harness) do(origin, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
	r.Host = strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Dyna", "1")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.web.ServeHTTP(w, r)
	return w
}

func (h *harness) mint(b *browser, claims map[string]any) (*httptest.ResponseRecorder, string) {
	c := map[string]any{"op": "mint", "sub": ownerID, "dev": devID, "jkt": b.jkt(), "origin": b.origin}
	for k, v := range claims {
		c[k] = v
	}
	nonce := randomToken(16)
	c["nonce"] = nonce
	r := httptest.NewRequest("POST", tunnelApp+"/connect/v1/mint", strings.NewReader(h.relayJWS(c)))
	r.Host = "m-abc.residentlabs.dev"
	w := httptest.NewRecorder()
	h.web.ServeHTTP(w, r)
	return w, nonce
}

// signIn runs the whole flow: mint, check the machine's signature, and
// exchange the code with a proof.
func (h *harness) signIn(b *browser) {
	h.t.Helper()
	w, nonce := h.mint(b, nil)
	if w.Code != 200 {
		h.t.Fatalf("mint: %d %s", w.Code, w.Body)
	}
	var out struct{ Code, Sig string }
	json.Unmarshal(w.Body.Bytes(), &out)
	sig, _ := b64.DecodeString(out.Sig)
	if !ed25519.Verify(h.svc.key.Public().(ed25519.PublicKey), []byte(MintMessage(nonce, out.Code)), sig) {
		h.t.Fatal("the mint answer is not signed by the machine key")
	}
	body, _ := json.Marshal(map[string]string{"code": out.Code, "proof": b.proof("/api/connect/session", h.clock())})
	res := h.do(b.origin, "POST", "/api/connect/session", string(body), nil)
	if res.Code != 200 {
		h.t.Fatalf("session: %d %s", res.Code, res.Body)
	}
	b.cookie = res.Result().Cookies()[0]
}

func (h *harness) renew(b *browser) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"proof": b.proof("/api/connect/renew", h.clock())})
	w := h.do(b.origin, "POST", "/api/connect/renew", string(body), b.cookie)
	if w.Code == 200 {
		b.cookie = w.Result().Cookies()[0]
	}
	return w
}

func TestSignInThroughTheTunnel(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	if b.cookie.Name != "__Host-dyna_connect" || !b.cookie.Secure || !b.cookie.HttpOnly || b.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", b.cookie)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 200 {
		t.Fatalf("Health: %d %s", w.Code, w.Body)
	}
	// Connect sessions get only their own method list.
	if w := h.do(tunnelApp, "POST", "/api/Secret", "[]", b.cookie); w.Code != 403 {
		t.Fatalf("Secret: %d", w.Code)
	}
	w := h.do(tunnelApp, "POST", "/api/connect/whoami", "{}", b.cookie)
	if !strings.Contains(w.Body.String(), `"kind":"connect"`) || !strings.Contains(w.Body.String(), `"user":"Sam"`) {
		t.Fatalf("whoami: %s", w.Body)
	}
}

func TestMintRules(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	if w, _ := h.mint(b, map[string]any{"sub": "usr_other"}); w.Code != 403 {
		t.Fatalf("another account: %d", w.Code)
	}
	if w, _ := h.mint(b, map[string]any{"aud": "other"}); w.Code != 403 {
		t.Fatalf("another machine: %d", w.Code)
	}
	if w, _ := h.mint(b, map[string]any{"origin": "https://evil.example"}); w.Code != 400 {
		t.Fatalf("unknown origin: %d", w.Code)
	}
	if w, _ := h.mint(b, map[string]any{"iat": h.clock().Add(-5 * time.Minute).Unix(), "exp": h.clock().Add(-4 * time.Minute).Unix()}); w.Code != 403 {
		t.Fatalf("expired: %d", w.Code)
	}
	if w, _ := h.mint(b, map[string]any{"op": "revoke"}); w.Code != 403 {
		t.Fatalf("wrong op: %d", w.Code)
	}
	// A forged relay key.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	real := h.relay.key
	h.relay.key = other
	if w, _ := h.mint(b, nil); w.Code != 403 {
		t.Fatalf("forged: %d", w.Code)
	}
	h.relay.key = real
	// The same request twice: the nonce is spent.
	jws := h.relayJWS(map[string]any{"op": "mint", "sub": ownerID, "dev": devID, "jkt": b.jkt(), "origin": tunnelApp})
	for i, want := range []int{200, 403} {
		r := httptest.NewRequest("POST", tunnelApp+"/connect/v1/mint", strings.NewReader(jws))
		r.Host = "m-abc.residentlabs.dev"
		w := httptest.NewRecorder()
		h.web.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("try %d: %d %s", i, w.Code, w.Body)
		}
	}
	// Mint answers only on the tunnel host.
	r := httptest.NewRequest("POST", tsApp+"/connect/v1/mint", strings.NewReader(h.relayJWS(map[string]any{"op": "mint"})))
	r.Host = "desk.example.ts.net"
	w := httptest.NewRecorder()
	h.web.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("tailscale host: %d", w.Code)
	}
}

func TestCodeIsOneUseAndBoundToTheKey(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	w, _ := h.mint(b, nil)
	var out struct{ Code string }
	json.Unmarshal(w.Body.Bytes(), &out)
	thief := newBrowser(tunnelApp)
	body, _ := json.Marshal(map[string]string{"code": out.Code, "proof": thief.proof("/api/connect/session", h.clock())})
	if res := h.do(tunnelApp, "POST", "/api/connect/session", string(body), nil); res.Code != 403 {
		t.Fatalf("thief: %d", res.Code)
	}
	// The failed try spent the code.
	body, _ = json.Marshal(map[string]string{"code": out.Code, "proof": b.proof("/api/connect/session", h.clock())})
	if res := h.do(tunnelApp, "POST", "/api/connect/session", string(body), nil); res.Code != 403 {
		t.Fatalf("reuse: %d", res.Code)
	}
}

func TestAccessExpiresAndRenews(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	h.advance(AccessLife + time.Second)
	w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie)
	if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"renew"`) {
		t.Fatalf("after access: %d %s", w.Code, w.Body)
	}
	old := b.cookie
	if w := h.renew(b); w.Code != 200 {
		t.Fatalf("renew: %d %s", w.Code, w.Body)
	}
	if b.cookie.Value == old.Value {
		t.Fatal("renew did not rotate the cookie")
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 200 {
		t.Fatalf("after renew: %d", w.Code)
	}
	// The old value still works for 30 seconds (requests in flight). After
	// that it only renews, and renew needs the key: a copied cookie gets
	// nothing, and a browser that lost the new value recovers.
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", old); w.Code != 200 {
		t.Fatalf("old value in grace: %d", w.Code)
	}
	h.advance(time.Minute)
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", old); w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"renew"`) {
		t.Fatalf("old value later: %d %s", w.Code, w.Body)
	}
	thief := &browser{key: newBrowser(tunnelApp).key, origin: tunnelApp, cookie: old}
	for i := 0; i < 12; i++ {
		if w := h.renew(thief); w.Code != 403 {
			t.Fatalf("old value with another key: %d", w.Code)
		}
	}
	// The thief's logout clears only its own cookie.
	if w := h.do(tunnelApp, "POST", "/api/logout", "[]", old); w.Code != 200 {
		t.Fatalf("stale logout: %d", w.Code)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 200 {
		t.Fatalf("session after a stale logout: %d", w.Code)
	}
	audit, _ := os.ReadFile(h.store.path("audit.log"))
	if n := strings.Count(string(audit), "session.old-cookie"); n != 1 {
		t.Fatalf("old-cookie audit lines: %d, want 1 per minute", n)
	}
	lost := &browser{key: b.key, origin: tunnelApp, cookie: old}
	if w := h.renew(lost); w.Code != 200 {
		t.Fatalf("old value with the right key: %d %s", w.Code, w.Body)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", lost.cookie); w.Code != 200 {
		t.Fatalf("after recovery: %d", w.Code)
	}
}

func TestRenewNeedsTheKey(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	thief := &browser{key: newBrowser(tunnelApp).key, origin: tunnelApp, cookie: b.cookie}
	if w := h.renew(thief); w.Code != 403 {
		t.Fatalf("renew with another key: %d", w.Code)
	}
}

func TestGrantRules(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)

	// Relay down: renew within the grace, logged; then refuse.
	h.relay.down = true
	h.advance(10 * time.Minute)
	if w := h.renew(b); w.Code != 200 || !strings.Contains(w.Body.String(), `"grace":true`) {
		t.Fatalf("grace renew: %d %s", w.Code, w.Body)
	}
	audit, _ := os.ReadFile(h.store.path("audit.log"))
	if !strings.Contains(string(audit), "session.grace") {
		t.Fatal("grace renew not in the audit log")
	}
	h.advance(10 * time.Minute)
	if w := h.renew(b); w.Code != 503 {
		t.Fatalf("past grace: %d %s", w.Code, w.Body)
	}
	// Relay back and says no: the session ends.
	h.relay.down = false
	h.relay.active = false
	if w := h.renew(b); w.Code != 401 || !strings.Contains(w.Body.String(), "connect-ended") {
		t.Fatalf("refused: %d %s", w.Code, w.Body)
	}
	if list, _ := h.store.Sessions(); len(list) != 0 {
		t.Fatalf("sessions left: %d", len(list))
	}
}

func TestRelayCongestionIsNotARefusal(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusRequestTimeout} {
		h := newHarness(t)
		b := newBrowser(tunnelApp)
		h.signIn(b)
		h.relay.status = code
		h.advance(2 * time.Minute)
		if w := h.renew(b); w.Code != 200 || !strings.Contains(w.Body.String(), `"grace":true`) {
			t.Fatalf("HTTP %d: renew %d %s", code, w.Code, w.Body)
		}
		// A real refusal still ends the session.
		h.relay.status = http.StatusForbidden
		h.advance(2 * time.Minute)
		if w := h.renew(b); w.Code != 401 {
			t.Fatalf("HTTP 403: renew %d %s", w.Code, w.Body)
		}
	}
}

func TestGrantCheckIsCached(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	h.renew(b)
	h.advance(10 * time.Second)
	h.renew(b)
	if h.relay.grants != 1 {
		t.Fatalf("grant checks: %d, want 1 within 60 s", h.relay.grants)
	}
	h.advance(2 * time.Minute)
	h.renew(b)
	if h.relay.grants != 2 {
		t.Fatalf("grant checks: %d, want 2", h.relay.grants)
	}
}

func TestGrantEndsAfter30Days(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	for i := 0; i < 29; i++ {
		h.advance(24 * time.Hour)
		if w := h.renew(b); w.Code != 200 {
			t.Fatalf("day %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	h.advance(24*time.Hour + time.Minute)
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 401 {
		t.Fatalf("after 30 days: %d", w.Code)
	}
	if w := h.renew(b); w.Code != 401 {
		t.Fatalf("renew after 30 days: %d %s", w.Code, w.Body)
	}
}

func TestOlderCookieValuesOnlyRenew(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	first := b.cookie
	h.renew(b)
	h.renew(b)
	// Two rotations later, within 30 seconds of the last: the oldest value
	// still only renews.
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", first); w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"renew"`) {
		t.Fatalf("value two back: %d %s", w.Code, w.Body)
	}
	lost := &browser{key: b.key, origin: tunnelApp, cookie: first}
	if w := h.renew(lost); w.Code != 200 {
		t.Fatalf("recover from two back: %d %s", w.Code, w.Body)
	}
}

func TestRelayRevokeAndEpoch(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	r := httptest.NewRequest("POST", tunnelApp+"/connect/v1/revoke", strings.NewReader(h.relayJWS(map[string]any{"op": "revoke", "dev": devID})))
	r.Host = "m-abc.residentlabs.dev"
	w := httptest.NewRecorder()
	h.web.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 401 {
		t.Fatalf("after revoke: %d", w.Code)
	}
	// An epoch bump ends every older session.
	b2 := newBrowser(tunnelApp)
	h.signIn(b2)
	r = httptest.NewRequest("POST", tunnelApp+"/connect/v1/revoke", strings.NewReader(h.relayJWS(map[string]any{"op": "revoke", "epoch": 2})))
	r.Host = "m-abc.residentlabs.dev"
	h.web.ServeHTTP(httptest.NewRecorder(), r)
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b2.cookie); w.Code != 401 {
		t.Fatalf("after epoch bump: %d", w.Code)
	}
}

func TestHandoffToAnotherRoute(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	src, _ := h.store.Sessions()
	srcEnd := src[0].End
	// A cookie with no key proof cannot open another origin.
	body := fmt.Sprintf(`{"target":%q}`, tsApp)
	if w := h.do(tunnelApp, "POST", "/api/connect/handoff", body, b.cookie); w.Code != 403 {
		t.Fatalf("no proof: %d", w.Code)
	}
	bad, _ := json.Marshal(map[string]string{"target": "https://evil.example", "proof": b.proof("/api/connect/handoff", h.clock())})
	if w := h.do(tunnelApp, "POST", "/api/connect/handoff", string(bad), b.cookie); w.Code != 400 {
		t.Fatalf("unknown target: %d", w.Code)
	}
	ok, _ := json.Marshal(map[string]string{"target": tsApp, "proof": b.proof("/api/connect/handoff", h.clock())})
	w := h.do(tunnelApp, "POST", "/api/connect/handoff", string(ok), b.cookie)
	if w.Code != 200 {
		t.Fatalf("handoff: %d %s", w.Code, w.Body)
	}
	var out struct{ Code string }
	json.Unmarshal(w.Body.Bytes(), &out)
	// The new origin has its own key.
	t2 := newBrowser(tsApp)
	ex, _ := json.Marshal(map[string]string{"handoff": out.Code, "proof": t2.proof("/api/connect/session", h.clock())})
	res := h.do(tsApp, "POST", "/api/connect/session", string(ex), nil)
	if res.Code != 200 {
		t.Fatalf("exchange: %d %s", res.Code, res.Body)
	}
	t2.cookie = res.Result().Cookies()[0]
	if w := h.do(tsApp, "POST", "/api/Health", "[]", t2.cookie); w.Code != 200 {
		t.Fatalf("on the new route: %d %s", w.Code, w.Body)
	}
	// The tunnel cookie does not work on the Tailscale origin.
	if w := h.do(tsApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 401 {
		t.Fatalf("cookie on the wrong origin: %d", w.Code)
	}
	// One use, and 60 seconds.
	if res := h.do(tsApp, "POST", "/api/connect/session", string(ex), nil); res.Code != 403 {
		t.Fatalf("reuse: %d", res.Code)
	}
	// The handoff moved the session: the source ended, so its cookie works
	// nowhere and one session remains for the device, keeping the source's
	// grant end.
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 401 {
		t.Fatalf("source session still lives: %d", w.Code)
	}
	list, _ := h.store.Sessions()
	if len(list) != 1 || list[0].Device != devID || list[0].JKT != t2.jkt() || !list[0].End.Equal(srcEnd) {
		t.Fatalf("sessions after the handoff = %+v", list)
	}
}

func TestOneLoginMakesOneSession(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	first := b.cookie
	h.signIn(b) // the relay minted again; the same browser signs in again
	list, err := h.store.Sessions()
	if err != nil || len(list) != 1 {
		t.Fatalf("one login must leave one session, got %+v err=%v", list, err)
	}
	if list[0].Device != devID {
		t.Fatalf("session device = %s, want %s", list[0].Device, devID)
	}
	// The older session's cookie is dead; the fresh one works.
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", first); w.Code != 401 {
		t.Fatalf("first cookie = %d, want 401", w.Code)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", b.cookie); w.Code != 200 {
		t.Fatalf("fresh cookie = %d %s", w.Code, w.Body)
	}
	audit, _ := os.ReadFile(h.store.path("audit.log"))
	if !strings.Contains(string(audit), "superseded by") {
		t.Fatal("the ended session is not in the audit log")
	}
}

func TestHelloIsSignedForTheRoute(t *testing.T) {
	h := newHarness(t)
	nonce := randomToken(16)
	r := httptest.NewRequest("GET", tsApp+"/connect/v1/hello?nonce="+nonce, nil)
	r.Host = "desk.example.ts.net"
	w := httptest.NewRecorder()
	h.web.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("hello: %d %v", w.Code, w.Header())
	}
	var out struct{ Machine, Origin, Nonce, Sig string }
	json.Unmarshal(w.Body.Bytes(), &out)
	sig, _ := b64.DecodeString(out.Sig)
	if out.Origin != tsApp || !ed25519.Verify(h.svc.key.Public().(ed25519.PublicKey), []byte(HelloMessage("abc", tsApp, nonce)), sig) {
		t.Fatalf("hello %+v", out)
	}
}

func TestMetaHasNoRouteList(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest("GET", tunnelApp+"/", nil)
	r.Host = "m-abc.residentlabs.dev"
	w := httptest.NewRecorder()
	h.web.ServeHTTP(w, r)
	body, _ := io.ReadAll(w.Body)
	if strings.Contains(string(body), "tail1") || !strings.Contains(string(body), "&#34;machine&#34;:&#34;abc&#34;") {
		t.Fatalf("page meta: %s", body)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self' http://127.0.0.1:7457 "+tsApp) {
		t.Fatalf("csp: %s", w.Header().Get("Content-Security-Policy"))
	}
}

func TestProofChecks(t *testing.T) {
	b := newBrowser(tunnelApp)
	now := time.Now()
	if _, err := verifyProof(b.proof("/x", now), tunnelApp+"/x", now); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyProof(b.proof("/x", now), tunnelApp+"/y", now); err == nil {
		t.Fatal("wrong htu passed")
	}
	if _, err := verifyProof(b.proof("/x", now.Add(-2*time.Minute)), tunnelApp+"/x", now); err == nil {
		t.Fatal("old proof passed")
	}
	p := b.proof("/x", now)
	if _, err := verifyProof(p[:len(p)-4]+"AAAA", tunnelApp+"/x", now); err == nil {
		t.Fatal("bad signature passed")
	}
}

func TestTailscaleServeParse(t *testing.T) {
	out := []byte(`{"TCP":{"443":{"HTTPS":true},"8443":{"HTTPS":true}},"Web":{"desk.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7457"}}},"desk.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7458"}}}}}`)
	r, ok := parseTailscaleServe(out, 7457, 7458)
	if !ok || r.App != "https://desk.example.ts.net" || r.Cards != "https://desk.example.ts.net:8443" || r.Kind != "tailscale" {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, ok := parseTailscaleServe(out, 9000, 7458); ok {
		t.Fatal("matched a port it does not serve")
	}
}

func TestOwnerSignInHoldsEveryScope(t *testing.T) {
	h := newHarness(t)
	b := newBrowser(tunnelApp)
	h.signIn(b)
	list, err := h.store.Sessions()
	if err != nil || len(list) != 1 {
		t.Fatalf("sessions = %+v err=%v", list, err)
	}
	grants, err := devicegrant.Open(filepath.Dir(h.store.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	// The mint proves the code came for this machine's owner, so the
	// session is the owner's own login: every scope, like a shell.
	g, ok, err := grants.Get("connect", list[0].ID)
	if err != nil || !ok || g.Legacy || !reflect.DeepEqual(g.Scopes, scope.All()) {
		t.Fatalf("grant = %+v ok=%v err=%v", g, ok, err)
	}
}

// mintBody mints a sign-in code for b and returns the exchange request body.
func (h *harness) mintBody(b *browser) string {
	h.t.Helper()
	w, _ := h.mint(b, nil)
	if w.Code != 200 {
		h.t.Fatalf("mint: %d %s", w.Code, w.Body)
	}
	var out struct{ Code string }
	json.Unmarshal(w.Body.Bytes(), &out)
	body, _ := json.Marshal(map[string]string{"code": out.Code, "proof": b.proof("/api/connect/session", h.clock())})
	return string(body)
}

// watchSessionFile records the sessions on disk each time the store reads its
// clock. UpdateSessions reads it once per update, before it writes, so the
// snapshots show every state a second writer could start from.
func (h *harness) watchSessionFile() func() [][]Session {
	var mu sync.Mutex
	var seen [][]Session
	h.store.now = func() time.Time {
		raw, err := os.ReadFile(filepath.Join(h.store.Dir(), "sessions.json"))
		var list []Session
		if err == nil && json.Unmarshal(raw, &list) == nil {
			mu.Lock()
			seen = append(seen, list)
			mu.Unlock()
		}
		return h.clock()
	}
	return func() [][]Session {
		mu.Lock()
		defer mu.Unlock()
		return append([][]Session(nil), seen...)
	}
}

func TestSecondSignInReplacesTheOldSessionInOneUpdate(t *testing.T) {
	h := newHarness(t)
	old := newBrowser(tunnelApp)
	h.signIn(old)
	snapshots := h.watchSessionFile()
	fresh := newBrowser(tunnelApp)
	h.signIn(fresh)
	for i, list := range snapshots() {
		if len(list) > 1 {
			t.Fatalf("state %d held %d sessions for one device at once: %+v", i, len(list), list)
		}
	}
	list, err := h.store.Sessions()
	if err != nil || len(list) != 1 {
		t.Fatalf("sessions after second sign-in: %+v, %v", list, err)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", old.cookie); w.Code == 200 {
		t.Fatalf("old cookie: %d", w.Code)
	}
	if w := h.do(tunnelApp, "POST", "/api/Health", "[]", fresh.cookie); w.Code != 200 {
		t.Fatalf("new cookie: %d", w.Code)
	}
}

// Two sign-ins for one device at the same time must leave exactly one
// session. The old code appended, then removed siblings in a second update,
// so each could remove the other. No seam sits between those two steps, so
// this test cannot force that interleaving; it holds for every schedule, and
// TestSecondSignInReplacesTheOldSessionInOneUpdate covers the one-update rule.
func TestConcurrentSignInsLeaveOneSession(t *testing.T) {
	h := newHarness(t)
	var bodies []string
	for range 8 {
		bodies = append(bodies, h.mintBody(newBrowser(tunnelApp)))
	}
	start := make(chan struct{})
	codes := make(chan int, len(bodies))
	for _, body := range bodies {
		go func() {
			<-start
			codes <- h.do(tunnelApp, "POST", "/api/connect/session", body, nil).Code
		}()
	}
	close(start)
	for range bodies {
		if code := <-codes; code != 200 {
			t.Fatalf("exchange: %d", code)
		}
	}
	list, err := h.store.Sessions()
	if err != nil || len(list) != 1 {
		t.Fatalf("sessions after concurrent sign-ins: %+v, %v", list, err)
	}
}
