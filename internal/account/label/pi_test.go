package label

import (
	"encoding/json"
	"testing"
)

type entry struct {
	Type   string `json:"type"`
	Key    string `json:"key"`
	Access string `json:"access"`
}

func TestPiDefaultLabel(t *testing.T) {
	token := testJWT(map[string]string{"email": "person@example.test"})
	for _, tc := range []struct {
		entry entry
		want  string
	}{
		{entry{Type: "oauth", Access: token}, "person@example.test"},
		{entry{Type: "oauth", Access: "opaque"}, "provider login"},
		{entry{Type: "api_key", Key: "secret1234"}, "provider · 1234"},
		{entry{Type: "api_key", Key: "1234"}, "provider key"},
		{entry{Type: "api_key", Key: "secret\n123"}, "provider key"},
	} {
		raw, err := json.Marshal(tc.entry)
		if err != nil {
			t.Fatal(err)
		}
		if got := PiDefaultLabel("provider", raw); got != tc.want {
			t.Errorf("label = %q, want %q", got, tc.want)
		}
	}
}
