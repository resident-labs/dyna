package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decodeB64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decrypt is the receiver side of RFC 8291, for tests only.
func decrypt(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt := body[:16]
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != 4096 {
		t.Fatalf("record size %d", rs)
	}
	idlen := int(body[20])
	if idlen != 65 {
		t.Fatalf("keyid length %d", idlen)
	}
	asBytes := body[21 : 21+idlen]
	ct := body[21+idlen:]
	asPub, err := ecdh.P256().NewPublicKey(asBytes)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, auth)
	info := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	info = append(info, asBytes...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	rec, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if rec[len(rec)-1] != 0x02 {
		t.Fatalf("bad delimiter %x", rec[len(rec)-1])
	}
	return rec[:len(rec)-1]
}

func newReceiver(t *testing.T) (*ecdh.PrivateKey, []byte, Subscription) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return priv, auth, Subscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/abc",
		P256dh:   b64.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     b64.EncodeToString(auth),
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	priv, auth, _ := newReceiver(t)
	for _, n := range []int{0, 1, 500, MaxPayload} {
		plain := bytes.Repeat([]byte("x"), n)
		body, err := encryptFresh(plain, priv.PublicKey(), auth)
		if err != nil {
			t.Fatal(err)
		}
		if got := decrypt(t, body, priv, auth); !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: plaintext differs", n)
		}
	}
	a, _ := encryptFresh([]byte("hi"), priv.PublicKey(), auth)
	b, _ := encryptFresh([]byte("hi"), priv.PublicKey(), auth)
	if bytes.Equal(a[:16], b[:16]) || bytes.Equal(a[21:86], b[21:86]) {
		t.Fatal("salt or ephemeral key repeated")
	}
	if _, err := encryptFresh(make([]byte, MaxPayload+1), priv.PublicKey(), auth); err == nil {
		t.Fatal("oversize payload accepted")
	}
}

// TestRFC8291Vector checks Appendix A of RFC 8291.
func TestRFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	auth := mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw")
	want := mustB64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	plain := []byte("When I grow up, I want to be a watermelon")
	got, err := encrypt(plain, uaPriv.PublicKey(), auth, asPriv, salt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("body differs from RFC 8291 Appendix A\n got %s\nwant %s", b64.EncodeToString(got), b64.EncodeToString(want))
	}
	if !bytes.Equal(decrypt(t, got, uaPriv, auth), plain) {
		t.Fatal("vector does not decrypt")
	}
}

func newVAPID(t *testing.T) (*VAPID, string) {
	t.Helper()
	dir := t.TempDir()
	v, err := LoadOrCreateVAPID(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return v, dir
}

func TestVAPID(t *testing.T) {
	v, dir := newVAPID(t)
	fi, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	pub := mustB64(t, v.PublicKey())
	if len(pub) != 65 || pub[0] != 4 {
		t.Fatalf("public key is %d bytes", len(pub))
	}
	// The key survives a reload.
	v2, err := LoadOrCreateVAPID(dir, "mailto:me@example.com")
	if err != nil || v2.PublicKey() != v.PublicKey() {
		t.Fatalf("reload: %v", err)
	}

	before := time.Now()
	h, err := v.Authorization("https://fcm.googleapis.com/fcm/send/secret-path")
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		t.Fatal(h)
	}
	jwt, k, ok := strings.Cut(rest, ", k=")
	if !ok || k != v.PublicKey() {
		t.Fatalf("bad k in %q", h)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal(jwt)
	}
	if hdr := string(mustB64(t, parts[0])); hdr != `{"typ":"JWT","alg":"ES256"}` {
		t.Fatal(hdr)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(mustB64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://fcm.googleapis.com" || claims.Sub != DefaultContact {
		t.Fatalf("claims %+v", claims)
	}
	exp := time.Unix(claims.Exp, 0)
	if d := exp.Sub(before); d < 11*time.Hour+59*time.Minute || d > 12*time.Hour+time.Minute {
		t.Fatalf("exp is %v ahead", d)
	}
	sig := mustB64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.Verify(pk, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("JWT signature does not verify")
	}
	if v2.contact != "mailto:me@example.com" {
		t.Fatal("contact not kept")
	}
}

func TestValidateEndpoint(t *testing.T) {
	ok := []string{
		"https://fcm.googleapis.com/fcm/send/x",
		"https://updates.push.services.mozilla.com/wpush/v2/x",
		"https://web.push.apple.com/x",
		"https://wns2-par02p.notify.windows.com/w/?token=x",
		"https://FCM.googleapis.com:443/x",
		"https://jmt17.google.com/fcm/send/x",
	}
	for _, u := range ok {
		if err := ValidateEndpoint(u); err != nil {
			t.Errorf("%s rejected: %v", u, err)
		}
	}
	bad := []string{
		"http://fcm.googleapis.com/x",
		"https://127.0.0.1/x",
		"https://[::1]/x",
		"https://localhost/x",
		"https://example.com/x",
		"https://evil.com/fcm.googleapis.com",
		"https://fcm.googleapis.com.evil.com/x",
		"https://evilfcm.googleapis.com/x",
		"https://mail.google.com/x",
		"https://jmt17.google.com.evil.com/x",
		"https://xjmt17.google.com/x",
		"https://push.services.mozilla.com/x",
		"https://.push.services.mozilla.com/x",
		"https://evilpush.services.mozilla.com/x",
		"https://notify.windows.com/x",
		"https://user:pw@fcm.googleapis.com/x",
		"https://user@fcm.googleapis.com/x",
		"https://fcm.googleapis.com:8443/x",
		"https://fcm.googleapis.com:80/x",
		"ftp://fcm.googleapis.com/x",
		"",
		"fcm.googleapis.com/x",
	}
	for _, u := range bad {
		if err := ValidateEndpoint(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestStore(t *testing.T) {
	s, dir := openStore(t)
	_, _, sub := newReceiver(t)
	if err := s.Subscribe("d1", sub, false); err != nil {
		t.Fatal(err)
	}
	sub2 := sub
	sub2.Endpoint = "https://web.push.apple.com/second"
	if err := s.Subscribe("d1", sub2, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe("d2", sub, false); err != nil {
		t.Fatal(err)
	}
	e, ok := mustGet(t, s, "d1")
	if !ok || e.Endpoint != sub2.Endpoint || !e.Names || e.Created.IsZero() {
		t.Fatalf("last subscribe did not win: %+v", e)
	}
	if len(mustList(t, s)) != 2 {
		t.Fatal("want 2 entries")
	}
	fi, err := os.Stat(filepath.Join(dir, "connect", "push", subsFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}

	if err := s.SetNames("d2", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNames("nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := mustGet(t, s2, "d2"); !e.Names || e.Endpoint != sub.Endpoint {
		t.Fatalf("reload lost data: %+v", e)
	}

	if n, err := s2.RemoveDevicesNotIn(func(id string) bool { return id == "d1" }); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if _, ok := mustGet(t, s2, "d2"); ok {
		t.Fatal("d2 survived")
	}
	if err := s2.Unsubscribe("d1"); err != nil {
		t.Fatal(err)
	}
	s3, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(mustList(t, s3)) != 0 {
		t.Fatal("unsubscribe not saved")
	}
}

func TestSubscribeValidation(t *testing.T) {
	s, _ := openStore(t)
	_, _, good := newReceiver(t)
	cases := map[string]func(*Subscription){
		"bad host":     func(x *Subscription) { x.Endpoint = "https://example.com/x" },
		"not a point":  func(x *Subscription) { x.P256dh = b64.EncodeToString(make([]byte, 65)) },
		"short key":    func(x *Subscription) { x.P256dh = "AAAA" },
		"short auth":   func(x *Subscription) { x.Auth = b64.EncodeToString(make([]byte, 15)) },
		"auth not b64": func(x *Subscription) { x.Auth = "!!" },
	}
	for name, mut := range cases {
		sub := good
		mut(&sub)
		if err := s.Subscribe("d", sub, false); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := s.Subscribe("", good, false); err == nil {
		t.Error("empty device ID accepted")
	}
	if len(mustList(t, s)) != 0 {
		t.Fatal("bad subscribe was stored")
	}
}

// pushServer starts a TLS server and a Sender that trusts it.
func pushServer(t *testing.T, h http.HandlerFunc) (*Sender, *Store, Subscription, *ecdh.PrivateKey, []byte) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	store, _ := openStore(t)
	store.allow = func(string) error { return nil }
	v, _ := newVAPID(t)
	snd := NewSender(store, v)
	client := srv.Client()
	client.Timeout = sendTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	snd.client = client
	priv, auth, sub := newReceiver(t)
	sub.Endpoint = srv.URL + "/secret/path"
	if err := store.Subscribe("dev", sub, false); err != nil {
		t.Fatal(err)
	}
	return snd, store, sub, priv, auth
}

func TestSendOK(t *testing.T) {
	var got http.Header
	var body []byte
	snd, _, _, priv, auth := pushServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		body = readAll(r)
		if r.Method != http.MethodPost || r.URL.Path != "/secret/path" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	})
	err := snd.Send(context.Background(), "dev", []byte("hello"), Options{TTL: time.Minute, Urgency: "high", Topic: "room-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Content-Encoding": "aes128gcm",
		"Content-Type":     "application/octet-stream",
		"Ttl":              "60",
		"Urgency":          "high",
		"Topic":            "room-1",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
	if a := got.Get("Authorization"); !strings.HasPrefix(a, "vapid t=") || !strings.Contains(a, ", k=") {
		t.Errorf("Authorization = %q", a)
	}
	if string(decrypt(t, body, priv, auth)) != "hello" {
		t.Fatal("server got the wrong plaintext")
	}

	// Defaults.
	if err := snd.Send(context.Background(), "dev", []byte("x"), Options{}); err != nil {
		t.Fatal(err)
	}
	if got.Get("Ttl") != "86400" || got.Get("Urgency") != "normal" || got.Get("Topic") != "" {
		t.Errorf("defaults: %v", got)
	}
}

func readAll(r *http.Request) []byte {
	var b bytes.Buffer
	b.ReadFrom(r.Body)
	return b.Bytes()
}

func TestSendGone(t *testing.T) {
	for _, code := range []int{404, 410} {
		snd, store, _, _, _ := pushServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		err := snd.Send(context.Background(), "dev", []byte("x"), Options{})
		if !errors.Is(err, ErrGone) {
			t.Fatalf("%d: got %v", code, err)
		}
		if _, ok := mustGet(t, store, "dev"); ok {
			t.Fatalf("%d: subscription not deleted", code)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error leaks the path: %v", err)
		}
	}
}

func TestSendErrorsKeep(t *testing.T) {
	for _, code := range []int{429, 500, 503} {
		snd, store, _, _, _ := pushServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		err := snd.Send(context.Background(), "dev", []byte("x"), Options{})
		var se *StatusError
		if !errors.As(err, &se) || se.Code != code {
			t.Fatalf("%d: got %v", code, err)
		}
		if _, ok := mustGet(t, store, "dev"); !ok {
			t.Fatalf("%d: subscription deleted", code)
		}
	}
}

func TestSendNoRedirect(t *testing.T) {
	var hits atomic.Int32
	snd, store, _, _, _ := pushServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			hits.Add(1)
		}
		http.Redirect(w, r, "/other", http.StatusFound)
	})
	err := snd.Send(context.Background(), "dev", []byte("x"), Options{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 302 {
		t.Fatalf("got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	if _, ok := mustGet(t, store, "dev"); !ok {
		t.Fatal("subscription deleted")
	}
	if NewSender(store, snd.vapid).client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("default client follows redirects")
	}
	if NewSender(store, snd.vapid).client.Timeout != 15*time.Second {
		t.Fatal("default timeout is not 15 s")
	}
}

func TestSendChecks(t *testing.T) {
	snd, store, _, _, _ := pushServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	ctx := context.Background()
	if err := snd.Send(ctx, "nobody", nil, Options{}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := snd.Send(ctx, "dev", make([]byte, MaxPayload+1), Options{}); err == nil {
		t.Fatal("oversize payload sent")
	}
	for _, o := range []Options{{Urgency: "low"}, {Topic: strings.Repeat("a", 33)}, {Topic: "a b"}, {TTL: -1}} {
		if err := snd.Send(ctx, "dev", nil, o); err == nil {
			t.Errorf("options %+v accepted", o)
		}
	}
	// Send time allowlist: the real check blocks the test server.
	store.allow = ValidateEndpoint
	err := snd.Send(ctx, "dev", []byte("x"), Options{})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("got %v", err)
	}
}

func TestSendAll(t *testing.T) {
	var n atomic.Int32
	snd, store, sub, _, _ := pushServer(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path == "/secret/path" {
			w.WriteHeader(201)
			return
		}
		w.WriteHeader(410)
	})
	sub2 := sub
	sub2.Endpoint = strings.Replace(sub.Endpoint, "/secret/path", "/dead", 1)
	if err := store.Subscribe("dev2", sub2, true); err != nil {
		t.Fatal(err)
	}
	res := snd.SendAll(context.Background(), func(e Entry) []byte {
		if e.Names {
			return []byte("named")
		}
		return []byte("plain")
	}, Options{})
	if len(res) != 2 || res[0].DeviceID != "dev" || res[0].Err != nil || !errors.Is(res[1].Err, ErrGone) {
		t.Fatalf("results: %+v", res)
	}
	if n.Load() != 2 {
		t.Fatalf("%d requests", n.Load())
	}
	if err := store.Subscribe("dev2", sub2, false); err != nil {
		t.Fatal(err)
	}
	n.Store(0)
	res = snd.SendAll(context.Background(), func(e Entry) []byte {
		if e.DeviceID == "dev" {
			return nil
		}
		return []byte("plain")
	}, Options{})
	if n.Load() != 1 {
		t.Fatalf("nil payload was sent or the other device was skipped: %d", n.Load())
	}
	if _, ok := mustGet(t, store, "dev2"); ok {
		t.Fatal("dev2 not deleted")
	}
}

func mustGet(t *testing.T, s *Store, id string) (Entry, bool) {
	t.Helper()
	e, ok, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return e, ok
}

func mustList(t *testing.T, s *Store) []Entry {
	t.Helper()
	entries, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
