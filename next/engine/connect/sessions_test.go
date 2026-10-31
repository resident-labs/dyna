package connect

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

type relayTransport struct {
	base http.RoundTripper
	down atomic.Bool
}

func (t *relayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.down.Load() {
		return nil, errors.New("relay offline")
	}
	return t.base.RoundTrip(r)
}

func TestRelayMintExchangeRenewAndReplay(t *testing.T) {
	now := time.Unix(1900000000, 0)
	relayPub, relayKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var relayStatus atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/machine/grant" || r.Header.Get("Authorization") == "" {
			t.Errorf("unexpected relay request %s", r.URL.Path)
			http.Error(w, "bad", 400)
			return
		}
		if status := relayStatus.Load(); status != 0 {
			http.Error(w, "relay error", int(status))
			return
		}
		_, _ = w.Write([]byte(`{"active":true,"epoch":1}`))
	}))
	defer relay.Close()
	files, err := OpenFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = files.Key(); err != nil {
		t.Fatal(err)
	}
	origin := "https://machine.example"
	link := Link{Relay: relay.URL, Machine: "machine", Owner: Owner{ID: "owner"}, App: origin}
	if err = files.SaveLink(link); err != nil {
		t.Fatal(err)
	}
	if err = files.SaveKeys(KeySet{Keys: []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "key", X: b64.EncodeToString(relayPub)}}}); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(filepath.Join(files.dir, "jwks.json"), now, now); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	unlinked, err := NewOperations(context.Background(), files, st, 0, 0).Status(context.Background())
	if err != nil || unlinked.Relay != "" {
		t.Fatalf("unlinked relay: %q %v", unlinked.Relay, err)
	}
	if err = st.InitializeConnectLink(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	client := relay.Client()
	transport := &relayTransport{base: client.Transport}
	client.Transport = transport
	service := NewSessions(files, st, func() time.Time { return now }, client)
	const helloNonce = "abcdefghijklmnop"
	hello := httptest.NewRecorder()
	service.ServePublic(hello, httptest.NewRequest("GET", origin+"/connect/v1/hello?nonce="+helloNonce, nil), origin, []string{origin})
	var greeting struct {
		Machine string `json:"machine"`
		Origin  string `json:"origin"`
		Nonce   string `json:"nonce"`
		Sig     string `json:"sig"`
		Relay   string `json:"relay"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(hello.Body.Bytes(), &greeting); err != nil || hello.Code != 200 || greeting.Relay != relay.URL {
		t.Fatalf("hello: %d %s %v", hello.Code, hello.Body.String(), err)
	}
	key, err := files.Key()
	if err != nil {
		t.Fatal(err)
	}
	if greeting.Key != b64.EncodeToString(key.Public().(ed25519.PublicKey)) {
		t.Fatal("hello returned the wrong public key")
	}
	status, err := NewOperations(context.Background(), files, st, 0, 0).Status(context.Background())
	if err != nil || status.Fingerprint != Fingerprint(key.Public().(ed25519.PublicKey)) || status.Relay != link.Relay {
		t.Fatalf("linked status: %+v %v", status, err)
	}
	signature, err := b64.DecodeString(greeting.Sig)
	if err != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte("dyna-connect-hello.v1\n"+greeting.Machine+"\n"+greeting.Origin+"\n"+greeting.Nonce), signature) {
		t.Fatal("invalid hello signature")
	}
	browser, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := browser.PublicKey.Bytes()
	jwk := ecJWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(pub[1:33]), Y: b64.EncodeToString(pub[33:])}
	proof := func(path, jti string) string {
		h, _ := json.Marshal(struct {
			Alg string `json:"alg"`
			Typ string `json:"typ"`
			JWK ecJWK  `json:"jwk"`
		}{"ES256", "dpop+jwt", jwk})
		c, _ := json.Marshal(proofClaims{Htm: "POST", Htu: origin + path, Iat: now.Unix(), Jti: jti})
		input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
		d := sha256.Sum256([]byte(input))
		r, s, e := ecdsa.Sign(rand.Reader, browser, d[:])
		if e != nil {
			t.Fatal(e)
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return input + "." + b64.EncodeToString(sig)
	}
	mint := func(nonce string) string {
		h, _ := json.Marshal(jwsHeader{Alg: "EdDSA", Kid: "key"})
		c, _ := json.Marshal(RelayClaims{Op: "mint", Iss: relay.URL, Aud: "machine", Sub: "owner", Dev: "dev_one", Epoch: 1, JKT: jwk.thumbprint(), Origin: origin, Nonce: nonce, Iat: now.Unix(), Exp: now.Add(time.Minute).Unix()})
		input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
		signed := input + "." + b64.EncodeToString(ed25519.Sign(relayKey, []byte(input)))
		r := httptest.NewRequest("POST", origin+"/connect/v1/mint", bytes.NewBufferString(signed))
		w := httptest.NewRecorder()
		service.ServePublic(w, r, origin, []string{origin})
		if w.Code != 200 {
			t.Fatalf("mint: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Code
	}
	exchange := func(code, p string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"code": code, "proof": p})
		r := httptest.NewRequest("POST", origin+"/api/connect/session", bytes.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Dyna", "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		service.ServePublic(w, r, origin, []string{origin})
		return w
	}
	bad := httptest.NewRequest("POST", origin+"/api/connect/session", bytes.NewBufferString("{}"))
	bad.Header.Set("Origin", origin)
	badResult := httptest.NewRecorder()
	service.ServePublic(badResult, bad, origin, []string{origin})
	if badResult.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF header: %d", badResult.Code)
	}
	code := mint("abcdefghijklmnop")
	p := proof("/api/connect/session", "abcdefghijklmnop")
	w := exchange(code, p)
	if w.Code != 200 {
		t.Fatalf("exchange: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("cookie: %+v", cookies)
	}
	second := mint("abcdefghijklmnopq")
	if w := exchange(second, p); w.Code != 403 {
		t.Fatalf("replayed proof: %d", w.Code)
	}
	r := httptest.NewRequest("POST", origin+"/api/connect/renew", bytes.NewBufferString(`{"proof":"`+proof("/api/connect/renew", "abcdefghijklmnopr")+`"}`))
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Dyna", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookies[0])
	x, _, ok, err := service.Authenticate(r.Context(), r, origin)
	if err != nil || !ok {
		t.Fatalf("authenticate: %v %v", ok, err)
	}
	out := httptest.NewRecorder()
	service.ServePrivate(out, r, origin, x, []string{origin})
	if out.Code != 200 {
		t.Fatalf("renew: %d %s", out.Code, out.Body.String())
	}
	if out.Result().Cookies()[0].Value == cookies[0].Value {
		t.Fatal("cookie did not rotate")
	}
	rotated := out.Result().Cookies()[0]
	now = now.Add(2 * time.Minute)
	transport.down.Store(true)
	r = httptest.NewRequest("POST", origin+"/api/connect/renew", bytes.NewBufferString(`{"proof":"`+proof("/api/connect/renew", "abcdefghijklmnopg")+`"}`))
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Dyna", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(rotated)
	x, _, ok, err = service.Authenticate(r.Context(), r, origin)
	if err != nil || !ok {
		t.Fatalf("grace auth: %v %v", ok, err)
	}
	out = httptest.NewRecorder()
	service.ServePrivate(out, r, origin, x, []string{origin})
	var renewal struct {
		Grace bool `json:"grace"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &renewal); err != nil || out.Code != 200 || !renewal.Grace {
		t.Fatalf("grace: %d %s %v", out.Code, out.Body.String(), err)
	}
	rotated = out.Result().Cookies()[0]
	transport.down.Store(false)
	relayStatus.Store(http.StatusServiceUnavailable)
	r = httptest.NewRequest("POST", origin+"/api/connect/renew", bytes.NewBufferString(`{"proof":"`+proof("/api/connect/renew", "abcdefghijklmnopi")+`"}`))
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Dyna", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(rotated)
	x, _, ok, err = service.Authenticate(r.Context(), r, origin)
	if err != nil || !ok {
		t.Fatalf("503 auth: %v %v", ok, err)
	}
	out = httptest.NewRecorder()
	service.ServePrivate(out, r, origin, x, []string{origin})
	if err := json.Unmarshal(out.Body.Bytes(), &renewal); err != nil || out.Code != 200 || !renewal.Grace {
		t.Fatalf("503 grace: %d %s %v", out.Code, out.Body.String(), err)
	}
	rotated = out.Result().Cookies()[0]
	now = now.Add(14 * time.Minute)
	r = httptest.NewRequest("POST", origin+"/api/connect/renew", bytes.NewBufferString(`{"proof":"`+proof("/api/connect/renew", "abcdefghijklmnoph")+`"}`))
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Dyna", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(rotated)
	x, _, ok, err = service.Authenticate(r.Context(), r, origin)
	if err != nil || !ok {
		t.Fatalf("expired grace auth: %v %v", ok, err)
	}
	out = httptest.NewRecorder()
	service.ServePrivate(out, r, origin, x, []string{origin})
	if out.Code != http.StatusServiceUnavailable {
		t.Fatalf("expired grace: %d", out.Code)
	}
	transport.down.Store(false)
	relayStatus.Store(http.StatusForbidden)
	r = httptest.NewRequest("POST", origin+"/api/connect/renew", bytes.NewBufferString(`{"proof":"`+proof("/api/connect/renew", "abcdefghijklmnops")+`"}`))
	r.Header.Set("Origin", origin)
	r.Header.Set("X-Dyna", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(rotated)
	x, _, ok, err = service.Authenticate(r.Context(), r, origin)
	if err != nil || !ok {
		t.Fatalf("rotated auth: %v %v", ok, err)
	}
	out = httptest.NewRecorder()
	service.ServePrivate(out, r, origin, x, []string{origin})
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("relay refusal: %d %s", out.Code, out.Body.String())
	}
	if _, _, ok, err = service.Authenticate(r.Context(), r, origin); err != nil || ok {
		t.Fatalf("revoked: %v %v", ok, err)
	}
}
