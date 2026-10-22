package realtime

import (
	"encoding/json"
	"os"
	"testing"
)

// The page and the engine share testdata/page-protocol.json: this test holds
// the Go side to it, and web/src/lib/voice/wire.contract.test.js the page's.
func TestPageProtocolMatchesTheSharedContract(t *testing.T) {
	data, err := os.ReadFile("testdata/page-protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		PageToEngine   []json.RawMessage `json:"pageToEngine"`
		EngineToPage   []json.RawMessage `json:"engineToPage"`
		CloseTakenOver int               `json:"closeTakenOver"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	same := func(kind string, want json.RawMessage, v any) {
		got, _ := json.Marshal(v)
		if canonical(want) != canonical(got) {
			t.Errorf("%s %s round-trips as %s", kind, want, got)
		}
	}
	for _, raw := range contract.PageToEngine {
		var m PageMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		same("page message", raw, m)
	}
	for _, raw := range contract.EngineToPage {
		var e PageEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		same("engine event", raw, e)
	}
	if contract.CloseTakenOver != CloseTakenOver {
		t.Errorf("takeover close code %d, the engine uses %d", contract.CloseTakenOver, CloseTakenOver)
	}
}

// canonical is data re-encoded, so key order and spaces do not count.
func canonical(data []byte) string {
	var v any
	_ = json.Unmarshal(data, &v)
	b, _ := json.Marshal(v)
	return string(b)
}
