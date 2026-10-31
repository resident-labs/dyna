package connect

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/resident-labs/dyna/engine/store"
)

func TestRelayVectorAndReplay(t *testing.T) {
	var vector struct {
		RelayPublic  string
		RelayRequest struct{ JWS string }
	}
	b, err := os.ReadFile("../../../testdata/connect/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &vector); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.InitializeConnectLink(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	keys := KeySet{Keys: []JWK{{Kid: "compat-1", Kty: "OKP", Crv: "Ed25519", X: vector.RelayPublic}}}
	now := time.Unix(1700000000, 0)
	verify := func() (RelayClaims, error) {
		c, err := VerifyRelay(vector.RelayRequest.JWS, "mint", "https://relay.test", "m_vector", keys, now)
		if err != nil {
			return c, err
		}
		return c, state.ConsumeConnectNonceForLink(context.Background(), c.Epoch, 1, c.Nonce, now, time.Unix(c.Exp, 0).Add(time.Minute))
	}
	c, err := verify()
	if err != nil || c.Epoch != 2 {
		t.Fatalf("claims = %+v, error = %v", c, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err = verify(); err == nil {
		t.Fatal("replay accepted after restart")
	}
}
