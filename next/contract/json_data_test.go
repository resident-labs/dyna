package contract

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONDataWire(t *testing.T) {
	for _, text := range []JSONData{`null`, `"null"`, `{"n":9007199254740993,"e":1e0,"zero":-0}`, `"\u00ff\ud800"`} {
		wire, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		var decoded JSONData
		if err := json.Unmarshal(wire, &decoded); err != nil || decoded != text {
			t.Fatalf("encoded text changed: %s %v", decoded, err)
		}
	}
	for _, wire := range []string{`null`, `42`, `{}`, `[]`, `true`} {
		var decoded JSONData
		if err := json.Unmarshal([]byte(wire), &decoded); err == nil {
			t.Fatalf("accepted a non-string data field: %s", wire)
		}
	}
	type payload struct {
		Data JSONData `json:"data,omitempty"`
	}
	absent, _ := json.Marshal(payload{})
	explicit, _ := json.Marshal(payload{Data: "null"})
	if !bytes.Equal(absent, []byte(`{}`)) || !bytes.Equal(explicit, []byte(`{"data":"null"}`)) {
		t.Fatalf("absence/null confused: %s %s", absent, explicit)
	}
}

func TestJSONDataWireRejectsInvalidUTF8(t *testing.T) {
	wire := append([]byte(`"\"`), 0xff)
	wire = append(wire, []byte(`\""`)...)
	var decoded JSONData
	if err := json.Unmarshal(wire, &decoded); err == nil {
		t.Fatal("invalid wire UTF-8 was silently replaced instead of rejected")
	}
}
