package connect

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

func TestHandoffReplacesLiveSourceOnce(t *testing.T) {
	now := time.Unix(1900000000, 0)
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.InitializeConnectLink(ctx, 1); err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := NewSessions(files, st, func() time.Time { return now }, nil)
	if err := files.SaveLink(Link{Machine: "machine", App: "https://tunnel.example"}); err != nil {
		t.Fatal(err)
	}
	listed := httptest.NewRecorder()
	s.routeList(listed, httptest.NewRequest("POST", "https://tunnel.example/api/connect/routes", nil), "https://tunnel.example",
		[]string{"https://tunnel.example", "http://remote.example", "https://custom.example", "https://host.ts.net", "http://localhost:3000"}, store.ConnectSession{Access: now.Add(time.Minute), End: now.Add(30 * time.Second)})
	var list struct {
		Routes  []string  `json:"routes"`
		Expires time.Time `json:"expires"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || (!list.Expires.Equal(now.Add(30*time.Second)) || !slices.Equal(list.Routes, []string{"http://localhost:3000", "https://host.ts.net", "https://custom.example", "https://tunnel.example"})) {
		t.Fatalf("route order: %s %v", listed.Body.String(), err)
	}
	makeProof := func(origin, path, nonce string) (string, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, _ := key.PublicKey.Bytes()
		jwk := ecJWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(pub[1:33]), Y: b64.EncodeToString(pub[33:])}
		h, _ := json.Marshal(struct {
			Alg string `json:"alg"`
			Typ string `json:"typ"`
			JWK ecJWK  `json:"jwk"`
		}{"ES256", "dpop+jwt", jwk})
		c, _ := json.Marshal(proofClaims{Htm: "POST", Htu: origin + path, Iat: now.Unix(), Jti: nonce})
		input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
		digest := sha256.Sum256([]byte(input))
		a, b, err := ecdsa.Sign(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig := make([]byte, 64)
		a.FillBytes(sig[:32])
		b.FillBytes(sig[32:])
		return input + "." + b64.EncodeToString(sig), jwk.thumbprint()
	}
	source, target := "https://source.example", "https://target.example"
	proof, jkt := makeProof(source, "/api/connect/handoff", "abcdefghijklmnop")
	x := store.ConnectSession{ID: "source", RelayDevice: "dev_one", Device: "device", Origin: source, JKT: jkt, User: "owner", Epoch: 1, Generation: 1, Hash: sha256.Sum256([]byte("cookie")), Created: now, Minted: now, End: now.Add(time.Hour), Access: now.Add(10 * time.Minute), Grant: now}
	if err := st.StartConnectSession(ctx, x); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", source+"/api/connect/handoff", nil)
	w := httptest.NewRecorder()
	s.issueHandoff(w, r, source, target, proof, x, []string{source, target})
	var result struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatalf("issue: %d %s %v", w.Code, w.Body.String(), err)
	}
	p, _ := makeProof(target, "/api/connect/session", "abcdefghijklmnopq")
	exchange := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"handoff": result.Code, "proof": p})
		r := httptest.NewRequest("POST", target+"/api/connect/session", bytes.NewReader(body))
		r.Header.Set("Origin", target)
		r.Header.Set("X-Dyna", "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServePublic(w, r, target, []string{source, target})
		return w
	}
	w = exchange()
	if w.Code != 200 {
		t.Fatalf("exchange: %d %s", w.Code, w.Body.String())
	}
	if _, _, ok, err := st.AuthenticateConnect(ctx, x.Hash, source, now); err != nil || ok {
		t.Fatalf("source still active: %v %v", ok, err)
	}
	cookie := w.Result().Cookies()[0]
	y, _, ok, err := st.AuthenticateConnect(ctx, sha256.Sum256([]byte(cookie.Value)), target, now)
	if err != nil || !ok || y.Device != x.Device || !y.End.Equal(x.End) || !y.Grant.Equal(x.Grant) {
		t.Fatalf("target: %+v %v %v", y, ok, err)
	}
	if w = exchange(); w.Code != 403 {
		t.Fatalf("replay: %d", w.Code)
	}
	// Expired and wrong-origin codes never start a session.
	p, _ = makeProof(target, "/api/connect/session", "abcdefghijklmnops")
	s.handoffs[hashToken(result.Code)] = handoffCode{y, target, now}
	if w = exchange(); w.Code != 403 {
		t.Fatalf("expired handoff: %d", w.Code)
	}
	s.handoffs[hashToken(result.Code)] = handoffCode{y, source, now.Add(time.Minute)}
	if w = exchange(); w.Code != 403 {
		t.Fatalf("wrong origin: %d", w.Code)
	}
	// A code for an ended source cannot replace a later session.
	p, _ = makeProof(target, "/api/connect/session", "abcdefghijklmnopr")
	s.handoffs[hashToken(result.Code)] = handoffCode{x, target, now.Add(time.Minute)}
	if w = exchange(); w.Code != 403 {
		t.Fatalf("ended source: %d", w.Code)
	}
	if _, _, ok, err := st.AuthenticateConnect(ctx, sha256.Sum256([]byte(cookie.Value)), target, now); err != nil || !ok {
		t.Fatalf("target lost after stale handoff: %v %v", ok, err)
	}
	// A configured route is required even when the requester holds the source key.
	w = httptest.NewRecorder()
	s.issueHandoff(w, r, source, "https://attacker.example", proof, x, []string{source, target})
	if w.Code != 400 {
		t.Fatalf("unconfigured target: %d", w.Code)
	}
}

func TestRouteKindRejectsInsecureRemote(t *testing.T) {
	for _, tc := range []struct {
		app  string
		want int
	}{
		{"http://remote.example", -1}, {"http://127.0.0.1:3000", 0},
		{"https://host.ts.net", 1}, {"https://custom.example", 2},
		{"https://tunnel.example", 3}, {"https://host.example/path", -1},
	} {
		if got := routeRank(tc.app, "https://tunnel.example"); got != tc.want {
			t.Errorf("%s: %d", tc.app, got)
		}
	}
}
