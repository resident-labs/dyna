package connect

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSharedProtocolVectors(t *testing.T) {
	var v struct {
		MachineSeed, MachinePublic, RelayPublic string
		Fingerprints                            []struct{ PublicKey, Fingerprint string }
		MachineRequest                          struct {
			Body   string
			Claims map[string]any
			JWS    string
		}
		RelayRequest struct {
			Claims relayClaims
			JWS    string
		}
		Mint        struct{ Nonce, Code, Message, Sig string }
		MachineInfo MachineInfo
	}
	data, err := os.ReadFile("../../testdata/connect/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(v.MachineInfo)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metadata, wire["machineInfo"]) {
		t.Fatalf("machine metadata fields differ from the shared vector: %s", encoded)
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := b64.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	key := ed25519.NewKeyFromSeed(decode(v.MachineSeed))
	if b64.EncodeToString(key.Public().(ed25519.PublicKey)) != v.MachinePublic {
		t.Fatal("machine public key differs")
	}
	for _, f := range v.Fingerprints {
		if got := Fingerprint(decode(f.PublicKey)); got != f.Fingerprint {
			t.Errorf("fingerprint = %s, want %s", got, f.Fingerprint)
		}
	}
	signed, err := signMachine(key, v.MachineRequest.Claims)
	if err != nil || signed != v.MachineRequest.JWS {
		t.Fatalf("machine request differs: %v", err)
	}
	if hashToken(v.MachineRequest.Body) != v.MachineRequest.Claims["bh"] {
		t.Fatal("machine body hash differs")
	}
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := v.MachineInfo
	if err := st.SaveLink(Link{Relay: m.Relay, Machine: m.ID, Owner: m.Owner, Epoch: m.Epoch}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(v.RelayRequest.Claims.Iat, 0)
	if err := st.SaveKeySet(KeySet{Keys: []JWK{{Kty: "OKP", Crv: "Ed25519", Kid: "compat-1", X: v.RelayPublic}}, Fetched: now}); err != nil {
		t.Fatal(err)
	}
	svc, err := New(Options{Store: st, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/connect/v1/mint", strings.NewReader(v.RelayRequest.JWS))
	_, claims, err := svc.relayRequest(req, "mint")
	if err != nil || claims != v.RelayRequest.Claims {
		t.Fatalf("relay claims = %+v, error = %v", claims, err)
	}
	if got := MintMessage(v.Mint.Nonce, v.Mint.Code); got != v.Mint.Message {
		t.Fatalf("mint message = %q", got)
	}
	sig := ed25519.Sign(key, []byte(MintMessage(v.Mint.Nonce, v.Mint.Code)))
	if b64.EncodeToString(sig) != v.Mint.Sig {
		t.Fatal("mint signature differs")
	}
}
