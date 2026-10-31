package connect

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

func TestLinkThroughFakeRelay(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var pub ed25519.PublicKey
	var tokenCalls int
	app := "https://machine.example;script-src"
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/jwks.json" {
			w.Write([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":"invented"}]}`))
			return
		}
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "DynaMachine ")
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Error("missing signature")
			return
		}
		payload, err := b64.DecodeString(parts[1])
		if err != nil {
			t.Error(err)
			return
		}
		var c machineClaims
		if err := json.Unmarshal(payload, &c); err != nil {
			t.Error(err)
			return
		}
		if c.Htu != r.URL.Path || c.Bh != hashToken(string(body)) || c.Iat != now.Unix() {
			t.Error("bad claims")
		}
		if r.URL.Path == "/v1/link/start" {
			var b startBody
			json.Unmarshal(body, &b)
			pub, _ = b64.DecodeString(b.Pubkey)
			if b.AppPort != 1001 || b.CardPort != 1002 {
				t.Error("wrong ports")
			}
		}
		sig, _ := b64.DecodeString(parts[2])
		if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
			t.Error("bad signature")
		}
		switch r.URL.Path {
		case "/v1/link/start":
			json.NewEncoder(w).Encode(LinkStart{ID: "link_test", Expires: now.Add(time.Minute).UnixMilli()})
		case "/v1/link/poll":
			json.NewEncoder(w).Encode(LinkPoll{Status: "linked", Machine: MachineInfo{ID: "m_test", Owner: Owner{ID: "usr_test"}, Epoch: 3, App: app}})
		case "/v1/machine/tunnel-token":
			tokenCalls++
			if tokenCalls == 1 {
				http.Error(w, "offline", http.StatusBadGateway)
				return
			}
			w.Write([]byte(`{"token":"invented"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()
	dir := t.TempDir()
	files, err := OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	ticks <- now
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts := LinkOptions{Name: "test", AppPort: 1001, CardPort: 1002, Ticks: ticks, Now: func() time.Time { return now }}
	link, err := LinkMachine(context.Background(), &Relay{Base: relay.URL, Now: func() time.Time { return now }}, files, state, opts)
	if err == nil {
		t.Fatal("accepted invalid managed origin")
	}
	app = "https://machine.example"
	ticks <- now
	link, err = LinkMachine(context.Background(), &Relay{Now: func() time.Time { return now }}, files, state, opts)
	if err == nil || link.Machine != "m_test" {
		t.Fatalf("link = %+v, error = %v", link, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	files, err = OpenFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	link, err = LinkMachine(context.Background(), &Relay{Now: func() time.Time { return now }}, files, state, opts)
	if err != nil || link.Machine != "m_test" || tokenCalls != 2 {
		t.Fatalf("link = %+v, calls = %d, error = %v", link, tokenCalls, err)
	}
	for _, name := range []string{"machine.key", "link.json", "tunnel-token", "jwks.json"} {
		info, err := os.Stat(filepath.Join(dir, "connect", name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
}
